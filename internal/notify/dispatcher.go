package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/metrics"
	"webmonitoring/internal/notifygate"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// notifyTracer is the no-op tracer until telemetry.Setup installs a provider.
var notifyTracer = otel.Tracer("webmonitoring/internal/notify")

type Dispatcher struct {
	repository  store.Repository
	guard       *security.URLGuard
	client      *http.Client
	metrics     *metrics.Registry
	maxAttempts int
	baseBackoff time.Duration
	smtp        SMTPConfig
	// gate bounds and suspends a channel that is being used to death or that has
	// stopped working. A nil gate is unlimited, which is what a test dispatcher
	// and an unconfigured deployment get.
	gate *notifygate.Gate
	// logger reports the side effects that have no caller to return an error to
	// -- a breaker-transition notice that could not be delivered, an audit row
	// that could not be written. They must never fail the delivery that
	// triggered them, so they are logged rather than propagated.
	logger *slog.Logger
}

// SMTPConfig holds the outbound SMTP relay settings for EMAIL alert channels.
// Credentials are supplied via environment, never stored on the channel.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

type AlertQueue interface {
	EnqueueAlert(context.Context, domain.AlertJob) error
}

func (d *Dispatcher) SetMetrics(registry *metrics.Registry) {
	d.metrics = registry
}

func (d *Dispatcher) SetRetryPolicy(maxAttempts int, baseBackoff time.Duration) {
	if maxAttempts > 0 {
		d.maxAttempts = maxAttempts
	}
	if baseBackoff > 0 {
		d.baseBackoff = baseBackoff
	}
}

func (d *Dispatcher) SetSMTP(cfg SMTPConfig) {
	d.smtp = cfg
}

// SetGate installs the per-channel limiter and circuit breaker. Without one
// every channel is unlimited and never suspended, which is what the tests and
// the embedded single-process mode run with.
func (d *Dispatcher) SetGate(gate *notifygate.Gate) {
	d.gate = gate
}

// Gate exposes the installed gate so the console can read a channel's breaker
// state without a second copy of the policy living in the API layer.
func (d *Dispatcher) Gate() *notifygate.Gate {
	return d.gate
}

func (d *Dispatcher) recordRefusal(channelType, reason string) {
	if d.metrics != nil {
		d.metrics.Inc("webmonitoring_alert_suppressed_total", map[string]string{
			"channel_type": channelType, "reason": reason,
		})
	}
}

func (d *Dispatcher) ProcessAlert(ctx context.Context, job domain.AlertJob) error {
	incident, err := d.repository.GetIncident(ctx, job.TenantID, job.IncidentID)
	if err != nil {
		return fmt.Errorf("load alert incident: %w", err)
	}
	asset, err := d.repository.GetAsset(ctx, job.TenantID, job.AssetID)
	if err != nil {
		return fmt.Errorf("load alert asset: %w", err)
	}
	failures := d.SendIncident(ctx, incident, asset, job.Reason)
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	return nil
}

func (d *Dispatcher) MarkDeadLetter(ctx context.Context, job domain.AlertJob) error {
	deliveries, _, err := d.repository.ListAlertDeliveries(ctx, job.TenantID, job.IncidentID, store.ListOptions{})
	if err != nil {
		return fmt.Errorf("load alert deliveries: %w", err)
	}
	now := time.Now().UTC()
	var failures []error
	for _, delivery := range deliveries {
		if delivery.Status == "DELIVERED" {
			continue
		}
		delivery.Status = "FAILED"
		delivery.LastError = "alert message exceeded the maximum delivery count"
		delivery.NextAttemptAt = nil
		delivery.UpdatedAt = now
		if err := d.repository.UpdateAlertDelivery(ctx, delivery); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func NewDispatcher(repository store.Repository, guard *security.URLGuard) *Dispatcher {
	transport := &http.Transport{
		Proxy:       nil,
		DialContext: guard.DialContext,
	}
	return &Dispatcher{
		repository: repository,
		guard:      guard,
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return errors.New("notification redirect limit exceeded")
				}
				_, _, err := guard.Validate(request.Context(), request.URL.String())
				return err
			},
		},
		maxAttempts: 3,
		baseBackoff: 250 * time.Millisecond,
		logger:      slog.Default(),
	}
}

// SetLogger installs the logger for side effects that cannot be returned to a
// caller. Unset it and they go to slog's default, which is never a reason to
// drop them silently.
func (d *Dispatcher) SetLogger(logger *slog.Logger) {
	if logger != nil {
		d.logger = logger
	}
}

// SendIncident delivers one notification about an incident to every enabled
// channel, and reports the channels that failed.
//
// reason names what happened -- opened, recurred, transitioned, commented -- and
// is part of the delivery's dedup key. An incident is now notified more than
// once: it is opened, then acknowledged, then resolved, and each of those is a
// message an operator wants. Keying only on the incident would make the later
// ones look like duplicates of the first and drop them without a trace.
func (d *Dispatcher) SendIncident(ctx context.Context, incident domain.Incident, asset domain.Asset, reason string) []error {
	failures := make([]error, 0)
	channels, err := d.repository.ListAlertChannelsWithSecrets(ctx, incident.TenantID)
	if err != nil {
		return []error{fmt.Errorf("load alert channels: %w", err)}
	}
	for _, channel := range channels {
		if !channel.Enabled {
			continue
		}
		// The gate is consulted before any delivery record is created. A refused
		// notification is not a delivery that failed -- recording one would fill
		// the table with rows for messages that were deliberately never sent, and
		// a later retry would find them and think it had work to do.
		if decision := d.gate.Allow(ctx, channel.ID); !decision.Allowed {
			d.recordRefusal(channel.Type, decision.Reason)
			failures = append(failures, refusalError(channel.ID, decision))
			continue
		}
		dedupKey := incident.ID + ":" + channel.ID + ":" + alertReasonKey(reason)
		now := time.Now().UTC()
		delivery := domain.AlertDelivery{
			ID: identity.New(), TenantID: incident.TenantID, IncidentID: incident.ID,
			ChannelID: channel.ID, DedupKey: dedupKey,
			Status: "PENDING", CreatedAt: now, UpdatedAt: now,
		}
		if err := d.repository.CreateAlertDelivery(ctx, delivery); err != nil {
			if errors.Is(err, store.ErrConflict) {
				existing, findErr := d.findDelivery(ctx, incident.TenantID, incident.ID, dedupKey)
				if findErr != nil {
					failures = append(failures, fmt.Errorf("channel %s delivery lookup: %w", channel.ID, findErr))
					continue
				}
				if existing.ID == "" {
					failures = append(failures, fmt.Errorf("channel %s delivery conflict without existing record", channel.ID))
					continue
				}
				if existing.Status == "DELIVERED" {
					continue
				}
				delivery = existing
			} else {
				failures = append(failures, fmt.Errorf("channel %s delivery record: %w", channel.ID, err))
				continue
			}
		}
		var lastErr error
		startAttempt := delivery.Attempts + 1
		if startAttempt < 1 {
			startAttempt = 1
		}
		if startAttempt > d.maxAttempts {
			continue
		}
		for attempt := startAttempt; attempt <= d.maxAttempts; attempt++ {
			if attempt > 1 {
				timer := time.NewTimer(time.Duration(1<<attempt) * d.baseBackoff)
				select {
				case <-ctx.Done():
					timer.Stop()
					lastErr = ctx.Err()
				case <-timer.C:
				}
				if ctx.Err() != nil {
					break
				}
			}
			delivery.Attempts = attempt
			delivery.NextAttemptAt = nil
			delivery.UpdatedAt = time.Now().UTC()
			if err := d.send(ctx, channel, incident, asset, reason); err == nil {
				delivery.Status = "DELIVERED"
				delivery.LastError = ""
				if updateErr := d.repository.UpdateAlertDelivery(ctx, delivery); updateErr != nil {
					failures = append(failures, fmt.Errorf("channel %s delivery update: %w", channel.ID, updateErr))
				}
				// A success is what closes the breaker, so it is recorded before
				// the loop can run again -- and only on the attempt that actually
				// reached the endpoint, not on the ones that backed off.
				d.recordOutcome(ctx, channel, true)
				d.record(channel.Type, "DELIVERED")
				lastErr = nil
				break
			} else {
				lastErr = err
				delivery.LastError = safeDeliveryError(err)
				delivery.NextAttemptAt = ptr(time.Now().UTC().Add(time.Duration(1<<attempt) * d.baseBackoff))
				d.record(channel.Type, "RETRY")
				if updateErr := d.repository.UpdateAlertDelivery(ctx, delivery); updateErr != nil {
					lastErr = errors.Join(lastErr, updateErr)
					break
				}
			}
		}
		if lastErr != nil {
			delivery.Status = "FAILED"
			delivery.LastError = safeDeliveryError(lastErr)
			delivery.UpdatedAt = time.Now().UTC()
			delivery.NextAttemptAt = nil
			if updateErr := d.repository.UpdateAlertDelivery(ctx, delivery); updateErr != nil {
				failures = append(failures, fmt.Errorf("channel %s delivery update: %w", channel.ID, updateErr))
			}
			// One failure per delivery, after the whole ladder has been spent,
			// not one per attempt: the breaker is counting dead channels, and a
			// single message that needed three tries is one channel that failed.
			d.recordOutcome(ctx, channel, false)
			d.record(channel.Type, "FAILED")
			failures = append(failures, fmt.Errorf("channel %s: %w", channel.ID, lastErr))
		}
	}
	return failures
}

// SendTest delivers one throwaway message to a channel so an operator can find
// out whether it is configured correctly, rather than waiting for a real
// incident and reading the delivery table to see what happened.
//
// It deliberately parts ways with SendIncident in three ways, and each is the
// point of the operation rather than an oversight:
//
//   - The gate is not consulted. The breaker refuses a channel precisely when
//     an operator most wants to test it -- that is what "has it recovered"
//     means -- and a test that the breaker could refuse would answer a question
//     nobody asked. It also does not feed an outcome back, because a probe the
//     operator triggered by hand is not evidence about the channel's automatic
//     delivery.
//   - No delivery record is written. The alert delivery table is the history of
//     what the platform actually alerted on; a manual test would sit in it as a
//     phantom incident-less row, and the retry worker would find it and treat it
//     as work.
//   - There is no retry ladder. A test reports what happened on the first
//     attempt, now, because the operator is waiting for the answer.
//
// It does not skip the SSRF guard: sendForChannel validates the target exactly
// as a real delivery would, and a test send is one more place a mistyped or
// hostile URL must not reach.
func (d *Dispatcher) SendTest(ctx context.Context, channel domain.AlertChannel, incident domain.Incident, asset domain.Asset) error {
	if !channel.Enabled {
		return errors.New("channel is disabled")
	}
	return d.send(ctx, channel, incident, asset, domain.AlertReasonTest)
}

func (d *Dispatcher) record(channelType, status string) {
	if d.metrics != nil {
		d.metrics.Inc("webmonitoring_alert_deliveries_total", map[string]string{
			"channel_type": channelType, "status": status,
		})
	}
}

// recordOutcome feeds a delivery result back to the breaker and, when the
// breaker has just changed state, tells someone.
//
// Both directions are news the operator has no other way to receive. A channel
// that has stopped working is invisible until an incident happens to need it,
// and the console shows the suspended state only to whoever is already looking
// at that channel's row. The recovery matters just as much: a breach that has
// closed is a channel whose alerts are flowing again, and without a notice
// "suspended" is a state nobody learns has ended.
//
// It never touches the caller's error path. The incident was delivered or it was
// not; whether the notice about a channel's health could itself be delivered is
// a different question, and letting it fail the incident would turn a reporting
// problem into a delivery problem.
func (d *Dispatcher) recordOutcome(ctx context.Context, channel domain.AlertChannel, delivered bool) {
	open, changed := d.gate.RecordOutcome(ctx, channel.ID, delivered)
	if !changed {
		return
	}
	action, event, headline, state := "monitoring.channel_restored", "monitoring.channel_restored", "告警渠道已恢复", "restored"
	if open {
		action, event, headline, state = "monitoring.channel_suspended", "monitoring.channel_suspended", "告警渠道已熔断", "suspended"
	}
	detail := fmt.Sprintf(
		"渠道：%s\n类型：%s\n状态：%s\n该渠道此前的告警可能没有送达——恢复后请确认这段时间的事件是否已被处理。",
		channel.Name, channel.Type, state,
	)
	// The channel itself is excluded: it is by definition the one that just
	// stopped carrying messages.
	if failures := d.SendOperationalNotice(ctx, channel.TenantID, event, headline, detail, channel.ID); len(failures) > 0 {
		// Not returned to the caller: see above. Logged so the notice is not
		// lost without trace.
		d.logger.Warn("channel health notice could not be delivered",
			"channel_id", channel.ID, "event", event, "failures", len(failures))
	}
	if err := d.repository.AppendAudit(ctx, domain.AuditEvent{
		ID: identity.New(), TenantID: channel.TenantID, Action: action,
		ResourceType: "alert_channel", ResourceID: channel.ID, Result: "SUCCESS",
		TraceID: "notify", Details: map[string]any{"channelType": channel.Type, "state": state},
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		d.logger.Warn("could not record the channel health audit", "channel_id", channel.ID, "error", err)
	}
}

// findDelivery looks up a channel's delivery by its dedup key rather than by
// channel alone: one incident now has several deliveries per channel, one per
// reason, and matching on the channel would find the wrong one.
func (d *Dispatcher) findDelivery(ctx context.Context, tenantID, incidentID, dedupKey string) (domain.AlertDelivery, error) {
	deliveries, _, err := d.repository.ListAlertDeliveries(ctx, tenantID, incidentID, store.ListOptions{})
	if err != nil {
		return domain.AlertDelivery{}, err
	}
	for _, delivery := range deliveries {
		if delivery.DedupKey == dedupKey {
			return delivery, nil
		}
	}
	return domain.AlertDelivery{}, nil
}

// alertReasonKey normalises a reason for use inside a dedup key. An empty one
// stands for the original behaviour -- an incident was created -- so a caller
// that does not name a reason and one that names CREATED produce the same key
// instead of two deliveries for the same event.
func alertReasonKey(reason string) string {
	if reason == "" {
		return domain.AlertReasonCreated
	}
	return reason
}

func ptr(value time.Time) *time.Time {
	return &value
}

func safeDeliveryError(err error) string {
	message := security.RedactString(err.Error())
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}

// refusalError renders a gate refusal as the error the caller sees. The two
// reasons get their own types so a caller can tell "we chose not to send" from
// "the channel is broken" without parsing a string.
func refusalError(channelID string, decision notifygate.Decision) error {
	if decision.Reason == "CIRCUIT_OPEN" {
		return &notifygate.ErrCircuitOpen{ChannelID: channelID, RetryAfter: decision.RetryAfter}
	}
	return &notifygate.ErrLimited{ChannelID: channelID, RetryAfter: decision.RetryAfter}
}
func (d *Dispatcher) send(
	ctx context.Context,
	channel domain.AlertChannel,
	incident domain.Incident,
	asset domain.Asset,
	reason string,
) error {
	// The span records the channel type and nothing else: a webhook URL can
	// carry a token, a DingTalk URL carries a signature, and recipients are
	// personal data. All of that stays out of the exported trace.
	ctx, span := notifyTracer.Start(ctx, "notify.send",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("webmonitoring.channel_type", channel.Type),
			attribute.String("webmonitoring.incident_id", incident.ID),
		))
	defer span.End()
	if err := d.sendForChannel(ctx, channel, incident, asset, reason); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "delivery failed")
		return err
	}
	return nil
}

func (d *Dispatcher) sendForChannel(
	ctx context.Context,
	channel domain.AlertChannel,
	incident domain.Incident,
	asset domain.Asset,
	reason string,
) error {
	if channel.Type == "EMAIL" {
		return d.sendEmail(ctx, channel, incident, asset, reason)
	}
	body, err := json.Marshal(buildAlertPayload(channel.Type, incident, asset, reason))
	if err != nil {
		return err
	}
	return d.postJSON(ctx, channel, body)
}

// postJSON delivers one already-rendered payload to a channel's endpoint.
//
// Every HTTP sender goes through here rather than repeating it, because this is
// where the SSRF guard, the DingTalk signature and the webhook HMAC live: a
// second copy would be a second place for a mistyped or hostile URL to reach the
// network, and a second place to forget to sign.
func (d *Dispatcher) postJSON(ctx context.Context, channel domain.AlertChannel, body []byte) error {
	if _, _, err := d.guard.Validate(ctx, channel.URL); err != nil {
		return fmt.Errorf("unsafe notification target: %w", err)
	}
	target := channel.URL
	if channel.Type == "DINGTALK" && channel.Secret != "" {
		target = signDingTalkURL(channel.URL, channel.Secret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "WebMonitoring-Notifier/0.1")
	if channel.Type == "WEBHOOK" && channel.Secret != "" {
		timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(channel.Secret))
		_, _ = mac.Write([]byte(timestamp + "\n"))
		_, _ = mac.Write(body)
		request.Header.Set("X-WebMonitoring-Timestamp", timestamp)
		request.Header.Set("X-WebMonitoring-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	response, err := d.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("notification endpoint returned %d", response.StatusCode)
	}
	return nil
}

// SendOperationalNotice delivers a platform-level message -- an asset whose
// checks keep failing, a channel whose breaker just opened -- to every enabled
// channel of a tenant.
//
// It deliberately does not go through SendIncident. Alert deliveries are keyed
// to an incident (alert_deliveries.incident_id is NOT NULL and references
// incidents), and a monitoring outage is not a tamper event: inventing an
// incident for it would put a row in the incident table that no evidence
// supports, which is the one thing this platform must not do. For the same
// reason there is no delivery row and no retry ladder here, exactly as in
// SendTest -- and for a further reason of its own: the condition being reported
// persists, so the caller raises the notice again on the next threshold
// crossing, which is a retry that cannot be lost with a queue.
//
// Unlike SendTest it does consult the gate, because this is an automatic
// message and stopping automatic messages is what the breaker is for. It does
// not feed its outcome back, and there are two reasons: the breaker's threshold
// is calibrated in incident alerts, which is the traffic it exists to bound; and
// a notice that moved the breaker could raise the breaker's own transition
// notice, which would be able to move it again.
//
// excludeChannelID skips a single channel, for the notice whose subject is that
// channel: a channel whose breaker just opened is the one channel that cannot
// carry the news.
func (d *Dispatcher) SendOperationalNotice(ctx context.Context, tenantID, event, headline, detail, excludeChannelID string) []error {
	failures := make([]error, 0)
	channels, err := d.repository.ListAlertChannelsWithSecrets(ctx, tenantID)
	if err != nil {
		return []error{fmt.Errorf("load alert channels: %w", err)}
	}
	for _, channel := range channels {
		if !channel.Enabled || channel.ID == excludeChannelID {
			continue
		}
		if decision := d.gate.Allow(ctx, channel.ID); !decision.Allowed {
			d.recordRefusal(channel.Type, decision.Reason)
			failures = append(failures, refusalError(channel.ID, decision))
			continue
		}
		var sendErr error
		if channel.Type == "EMAIL" {
			sendErr = d.sendEmailMessage(ctx, channel, headline, detail)
		} else {
			body, marshalErr := json.Marshal(buildNoticePayload(channel.Type, event, headline, detail))
			if marshalErr != nil {
				failures = append(failures, fmt.Errorf("channel %s notice payload: %w", channel.ID, marshalErr))
				continue
			}
			sendErr = d.postJSON(ctx, channel, body)
		}
		if sendErr != nil {
			d.record(channel.Type, "FAILED")
			failures = append(failures, fmt.Errorf("channel %s: %w", channel.ID, sendErr))
			continue
		}
		d.record(channel.Type, "DELIVERED")
	}
	return failures
}

// alertHeadline renders a reason as the first line of a notification. It is the
// only thing that tells an operator why they are being paged for the second time
// about an incident they already know about, so an unrecognised reason is echoed
// verbatim rather than collapsed into a generic "update".
func alertHeadline(reason string) string {
	switch alertReasonKey(reason) {
	case domain.AlertReasonCreated:
		return "网页风险告警"
	case domain.AlertReasonRecurred:
		return "网页风险告警（事件复现）"
	case domain.AlertReasonTransitioned:
		return "网页风险事件状态变更"
	case domain.AlertReasonCommented:
		return "网页风险事件新增备注"
	case domain.AlertReasonTest:
		return "网页监测告警渠道测试"
	default:
		return "网页风险告警（" + reason + "）"
	}
}

func buildAlertPayload(channelType string, incident domain.Incident, asset domain.Asset, reason string) any {
	headline := alertHeadline(reason)
	content := fmt.Sprintf(
		"**%s**\n>资产：%s\n>风险：%s\n>状态：%s\n>事件：%s",
		headline,
		asset.Name,
		incident.Severity,
		incident.Status,
		incident.Title,
	)
	switch channelType {
	case "WECHAT_WORK":
		return map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]string{"content": content},
		}
	case "DINGTALK":
		return map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"title": headline,
				"text":  content,
			},
		}
	default:
		return map[string]any{
			"event":  "incident." + strings.ToLower(alertReasonKey(reason)),
			"reason": alertReasonKey(reason),
			"data": map[string]any{
				"incidentId": incident.ID,
				"assetId":    asset.ID,
				"assetName":  asset.Name,
				"severity":   incident.Severity,
				"status":     incident.Status,
				"title":      incident.Title,
				"createdAt":  incident.CreatedAt,
			},
		}
	}
}

// signDingTalkURL appends the timestamp and HMAC-SHA256 signature that DingTalk
// requires for a robot webhook configured with the "加签" secret.
func signDingTalkURL(target, secret string) string {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "\n" + secret))
	sign := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	separator := "&"
	if !strings.Contains(target, "?") {
		separator = "?"
	}
	return target + separator + "timestamp=" + timestamp + "&sign=" + sign
}

func (d *Dispatcher) sendEmail(ctx context.Context, channel domain.AlertChannel, incident domain.Incident, asset domain.Asset, reason string) error {
	return d.sendEmailMessage(ctx, channel,
		alertHeadline(reason)+"："+incident.Title,
		fmt.Sprintf(
			"资产：%s\n风险等级：%s\n状态：%s\n事件：%s\n事件 ID：%s\n",
			asset.Name, incident.Severity, incident.Status, incident.Title, incident.ID,
		))
}

// sendEmailMessage delivers one already-rendered message. The SMTP mechanics
// live here rather than in each caller so the configuration checks, the
// cancellation handling and the header encoding exist once.
func (d *Dispatcher) sendEmailMessage(ctx context.Context, channel domain.AlertChannel, subject, body string) error {
	return d.sendEmailTo(ctx, splitRecipients(channel.Recipients), subject, body)
}

// SendUserNotice emails one person directly instead of fanning out to the
// tenant's channels.
//
// An assignment notice is addressed to whoever was handed the incident, and no
// channel is scoped to a person: the channels are the tenant's, so sending there
// would tell everyone and reach the assignee only if they happen to watch the
// same channel. It reports a missing configuration or address as an error rather
// than silently doing nothing, so the caller can log that the notice did not go
// out -- which is the difference between "nobody was told" and "we do not know".
func (d *Dispatcher) SendUserNotice(ctx context.Context, to, subject, body string) error {
	recipient := strings.TrimSpace(to)
	if recipient == "" {
		return errors.New("no address to notify")
	}
	return d.sendEmailTo(ctx, []string{recipient}, subject, body)
}

// sendEmailTo is the SMTP mechanics, shared by channel delivery and direct
// notices. The configuration checks, cancellation handling and header encoding
// live here so both paths behave identically.
func (d *Dispatcher) sendEmailTo(ctx context.Context, recipients []string, subject, body string) error {
	cfg := d.smtp
	if cfg.Host == "" {
		return errors.New("SMTP is not configured for email notifications")
	}
	if len(recipients) == 0 {
		return errors.New("email channel has no recipients")
	}
	from := cfg.From
	if from == "" {
		from = cfg.Username
	}
	message := buildEmailMessage(from, recipients, subject, body)
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}
	done := make(chan error, 1)
	go func() { done <- smtp.SendMail(addr, auth, from, recipients, message) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func splitRecipients(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func buildEmailMessage(from string, recipients []string, subject, body string) []byte {
	var header bytes.Buffer
	fmt.Fprintf(&header, "From: %s\r\n", from)
	fmt.Fprintf(&header, "To: %s\r\n", strings.Join(recipients, ", "))
	fmt.Fprintf(&header, "Subject: %s\r\n", mime.QEncoding.Encode("UTF-8", subject))
	header.WriteString("MIME-Version: 1.0\r\n")
	header.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	header.WriteString("\r\n")
	return append(header.Bytes(), []byte(body)...)
}

// buildNoticePayload renders a platform-level notice for one channel type. The
// webhook shape carries `event` so a receiver can route it without parsing
// Chinese prose, and carries no incidentId: there is no incident, and emitting a
// placeholder would invite a receiver to treat it as one.
func buildNoticePayload(channelType, event, headline, detail string) any {
	content := fmt.Sprintf("**%s**\n>%s", headline, strings.ReplaceAll(detail, "\n", "\n>"))
	switch channelType {
	case "WECHAT_WORK":
		return map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]string{"content": content},
		}
	case "DINGTALK":
		return map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"title": headline,
				"text":  content,
			},
		}
	default:
		return map[string]any{
			"event": event,
			"data": map[string]any{
				"headline": headline,
				"detail":   detail,
				"at":       time.Now().UTC(),
			},
		}
	}
}
