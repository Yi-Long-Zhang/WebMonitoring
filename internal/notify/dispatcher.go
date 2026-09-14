package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/metrics"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

type Dispatcher struct {
	repository  store.Repository
	guard       *security.URLGuard
	client      *http.Client
	metrics     *metrics.Registry
	maxAttempts int
	baseBackoff time.Duration
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

func (d *Dispatcher) ProcessAlert(ctx context.Context, job domain.AlertJob) error {
	incident, err := d.repository.GetIncident(job.TenantID, job.IncidentID)
	if err != nil {
		return fmt.Errorf("load alert incident: %w", err)
	}
	asset, err := d.repository.GetAsset(job.TenantID, job.AssetID)
	if err != nil {
		return fmt.Errorf("load alert asset: %w", err)
	}
	failures := d.SendIncident(ctx, incident, asset)
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	return nil
}

func (d *Dispatcher) MarkDeadLetter(ctx context.Context, job domain.AlertJob) error {
	deliveries, err := d.repository.ListAlertDeliveries(job.TenantID, job.IncidentID)
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
		if err := d.repository.UpdateAlertDelivery(delivery); err != nil {
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
	}
}

func (d *Dispatcher) SendIncident(ctx context.Context, incident domain.Incident, asset domain.Asset) []error {
	failures := make([]error, 0)
	channels, err := d.repository.ListAlertChannelsWithSecrets(incident.TenantID)
	if err != nil {
		return []error{fmt.Errorf("load alert channels: %w", err)}
	}
	for _, channel := range channels {
		if !channel.Enabled {
			continue
		}
		now := time.Now().UTC()
		delivery := domain.AlertDelivery{
			ID: identity.New(), TenantID: incident.TenantID, IncidentID: incident.ID,
			ChannelID: channel.ID, DedupKey: incident.ID + ":" + channel.ID,
			Status: "PENDING", CreatedAt: now, UpdatedAt: now,
		}
		if err := d.repository.CreateAlertDelivery(delivery); err != nil {
			if errors.Is(err, store.ErrConflict) {
				existing, findErr := d.findDelivery(incident.TenantID, incident.ID, channel.ID)
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
			if err := d.send(ctx, channel, incident, asset); err == nil {
				delivery.Status = "DELIVERED"
				delivery.LastError = ""
				if updateErr := d.repository.UpdateAlertDelivery(delivery); updateErr != nil {
					failures = append(failures, fmt.Errorf("channel %s delivery update: %w", channel.ID, updateErr))
				}
				d.record(channel.Type, "DELIVERED")
				lastErr = nil
				break
			} else {
				lastErr = err
				delivery.LastError = safeDeliveryError(err)
				delivery.NextAttemptAt = ptr(time.Now().UTC().Add(time.Duration(1<<attempt) * d.baseBackoff))
				d.record(channel.Type, "RETRY")
				if updateErr := d.repository.UpdateAlertDelivery(delivery); updateErr != nil {
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
			if updateErr := d.repository.UpdateAlertDelivery(delivery); updateErr != nil {
				failures = append(failures, fmt.Errorf("channel %s delivery update: %w", channel.ID, updateErr))
			}
			d.record(channel.Type, "FAILED")
			failures = append(failures, fmt.Errorf("channel %s: %w", channel.ID, lastErr))
		}
	}
	return failures
}

func (d *Dispatcher) record(channelType, status string) {
	if d.metrics != nil {
		d.metrics.Inc("webmonitoring_alert_deliveries_total", map[string]string{
			"channel_type": channelType, "status": status,
		})
	}
}

func (d *Dispatcher) findDelivery(tenantID, incidentID, channelID string) (domain.AlertDelivery, error) {
	deliveries, err := d.repository.ListAlertDeliveries(tenantID, incidentID)
	if err != nil {
		return domain.AlertDelivery{}, err
	}
	for _, delivery := range deliveries {
		if delivery.ChannelID == channelID {
			return delivery, nil
		}
	}
	return domain.AlertDelivery{}, nil
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
func (d *Dispatcher) send(
	ctx context.Context,
	channel domain.AlertChannel,
	incident domain.Incident,
	asset domain.Asset,
) error {
	if _, _, err := d.guard.Validate(ctx, channel.URL); err != nil {
		return fmt.Errorf("unsafe notification target: %w", err)
	}
	var payload any
	if channel.Type == "WECHAT_WORK" {
		payload = map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"content": fmt.Sprintf(
					"**网页风险告警**\n>资产：%s\n>风险：%s\n>事件：%s",
					asset.Name,
					incident.Severity,
					incident.Title,
				),
			},
		}
	} else {
		payload = map[string]any{
			"event": "incident.created",
			"data": map[string]any{
				"incidentId": incident.ID,
				"assetId":    asset.ID,
				"assetName":  asset.Name,
				"severity":   incident.Severity,
				"title":      incident.Title,
				"createdAt":  incident.CreatedAt,
			},
		}
	}
	body, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, channel.URL, bytes.NewReader(body))
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
