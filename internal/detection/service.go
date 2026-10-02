package detection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/events"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/metrics"
	"webmonitoring/internal/notify"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"

	"go.opentelemetry.io/otel"
	// Aliased because this package already has an `attribute` helper for HTML
	// nodes (html.go), which must not be shadowed by the tracing attributes.
	otelattr "go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// detectionTracer is the no-op tracer until telemetry.Setup installs a provider.
var detectionTracer = otel.Tracer("webmonitoring/internal/detection")

// Queue rejections the caller can act on, as opposed to internal failures. They
// are sentinels so the API can answer with a precise status and a fixed message
// instead of echoing a repository error back to the client.
var (
	ErrAssetNotVerified   = errors.New("asset must be enabled and verified before detection")
	ErrNoMonitoringPolicy = errors.New("asset has no monitoring policy")
	ErrInvalidMode        = errors.New("mode must be HTTP or BROWSER")
)

type BrowserChecker interface {
	Check(context.Context, domain.Asset, domain.MonitorPolicy) (CheckResult, error)
}

type JobQueue interface {
	Enqueue(context.Context, domain.DetectionJob) error
}

type AlertQueue interface {
	EnqueueAlert(context.Context, domain.AlertJob) error
}

type Service struct {
	repository        store.Repository
	evidence          evidence.Store
	http              *Checker
	browser           BrowserChecker
	notifier          *notify.Dispatcher
	broker            *events.Broker
	logger            *slog.Logger
	queue             JobQueue
	alertQueue        AlertQueue
	metrics           *metrics.Registry
	retryBase         time.Duration
	evidenceRetention time.Duration
}

func NewService(
	repository store.Repository,
	evidenceStore evidence.Store,
	httpChecker *Checker,
	browserChecker BrowserChecker,
	notifier *notify.Dispatcher,
	broker *events.Broker,
	logger *slog.Logger,
) *Service {
	return &Service{
		repository:        repository,
		evidence:          evidenceStore,
		http:              httpChecker,
		browser:           browserChecker,
		notifier:          notifier,
		broker:            broker,
		logger:            logger,
		retryBase:         500 * time.Millisecond,
		evidenceRetention: 90 * 24 * time.Hour,
	}
}

func (s *Service) SetQueue(queue JobQueue) {
	s.queue = queue
}

func (s *Service) SetAlertQueue(queue AlertQueue) {
	s.alertQueue = queue
}

// SuspendedChannels reports which of the given channel ids currently have an
// open notification circuit breaker, so the console can show why a channel has
// gone quiet. It is read-only: it consumes no allowance and does not probe.
//
// It goes through the service rather than the API reaching for the notifier
// because the service is what owns the notifier, and because a deployment that
// delivers through the alert worker has no notifier here at all -- in which case
// there is no gate to read and every channel reports as healthy, which is the
// honest answer rather than a misleading one.
func (s *Service) SuspendedChannels(ctx context.Context, channelIDs []string) map[string]bool {
	suspended := make(map[string]bool, len(channelIDs))
	if s.notifier == nil {
		return suspended
	}
	gate := s.notifier.Gate()
	if gate == nil {
		return suspended
	}
	for _, id := range channelIDs {
		if gate.Open(ctx, id) {
			suspended[id] = true
		}
	}
	return suspended
}

// NotifyIncident raises a notification about an incident that something other
// than a detection run has just changed -- an operator transitioning it, or
// adding a note. It exists as one method on this service rather than as the API
// reaching for the notifier directly because the service already owns the two
// ways a notification can be raised (the shared queue, or the dispatcher
// inline) and the choice between them must not depend on which caller asked.
//
// It returns nil rather than an error when there is nowhere to send: a
// deployment with no channels and no notifier is a valid configuration, and
// failing a status transition because nobody could be told about it would be
// the wrong trade. Delivery failures are logged, not returned, for the same
// reason -- the incident has already changed by the time this runs.
func (s *Service) NotifyIncident(ctx context.Context, incident domain.Incident, reason string) {
	// The queue is checked first, and on its own: a deployment that routes
	// notifications through the alert worker has no dispatcher on this service,
	// so requiring one here would mean the queue was never used at all.
	if s.alertQueue != nil {
		if err := s.alertQueue.EnqueueAlert(ctx, domain.AlertJob{
			TenantID: incident.TenantID, IncidentID: incident.ID, AssetID: incident.AssetID,
			Reason: reason,
		}); err != nil {
			s.logger.Warn("enqueue incident notification failed",
				"incident_id", incident.ID, "reason", reason, "error", err)
		}
		return
	}
	if s.notifier == nil {
		return
	}
	asset, err := s.repository.GetAsset(ctx, incident.TenantID, incident.AssetID)
	if err != nil {
		s.logger.Warn("load asset for notification failed",
			"incident_id", incident.ID, "reason", reason, "error", err)
		return
	}
	for _, notifyErr := range s.notifier.SendIncident(ctx, incident, asset, reason) {
		s.logger.Warn("notification failed",
			"incident_id", incident.ID, "reason", reason, "error", notifyErr)
	}
}

// TestChannel sends one throwaway message to a single channel so an operator can
// find out whether it is configured correctly without waiting for a real
// incident and reading the delivery table to see what happened.
//
// It is the one notification path that does not go through the queue. A test is
// a question the operator is waiting on the answer to, and the queue is
// fire-and-forget by design -- routing a test through it would return success
// for a message that had not been attempted yet, which is worse than no test at
// all. So this requires the dispatcher to be here, and says so plainly when it
// is not, rather than reporting a success that never happened.
//
// It returns the delivery error rather than logging it, unlike every other
// method here: the operator asked, and a failure is the answer.
func (s *Service) TestChannel(ctx context.Context, tenantID, channelID string) error {
	if s.notifier == nil {
		return errors.New("this deployment has no inline notifier to test through")
	}
	channel, err := s.repository.GetAlertChannel(ctx, tenantID, channelID)
	if err != nil {
		return err
	}
	return s.notifier.SendTest(ctx, channel, domain.Incident{}, domain.Asset{})
}

func (s *Service) SetMetrics(registry *metrics.Registry) {
	s.metrics = registry
}

func (s *Service) SetRetryBase(delay time.Duration) {
	if delay > 0 {
		s.retryBase = delay
	}
}

func (s *Service) SetEvidenceRetention(delay time.Duration) {
	if delay > 0 {
		s.evidenceRetention = delay
	}
}

func (s *Service) Queue(ctx context.Context, tenantID, assetID, mode string) (domain.DetectionJob, error) {
	asset, err := s.repository.GetAsset(ctx, tenantID, assetID)
	if err != nil {
		return domain.DetectionJob{}, err
	}
	if !asset.Enabled || asset.VerificationStatus != domain.VerificationVerified {
		return domain.DetectionJob{}, ErrAssetNotVerified
	}
	policy, err := s.repository.GetPolicy(ctx, tenantID, assetID)
	if err != nil {
		// Only a genuinely absent policy is a rejection the caller can act on;
		// a repository failure must stay a failure, not be reported as config.
		if errors.Is(err, store.ErrNotFound) {
			return domain.DetectionJob{}, ErrNoMonitoringPolicy
		}
		return domain.DetectionJob{}, err
	}
	mode = strings.ToUpper(mode)
	if mode == "" {
		mode = "HTTP"
	}
	if mode != "HTTP" && mode != "BROWSER" {
		return domain.DetectionJob{}, ErrInvalidMode
	}
	job := domain.DetectionJob{
		ID:            identity.New(),
		TenantID:      tenantID,
		AssetID:       assetID,
		Mode:          mode,
		Status:        domain.JobQueued,
		PolicyVersion: policy.Version,
		MaxAttempts:   3,
		CreatedAt:     time.Now().UTC(),
	}
	if err := s.repository.CreateJob(ctx, job); err != nil {
		return domain.DetectionJob{}, err
	}
	if s.metrics != nil {
		s.metrics.Inc("webmonitoring_detection_jobs_total", map[string]string{"mode": mode, "status": "QUEUED"})
	}
	s.broker.Publish("detection.job.queued", job)
	if s.queue != nil {
		if err := s.queue.Enqueue(ctx, job); err != nil {
			_ = s.failJob(ctx, job, "QUEUE_FAILED", err)
			return domain.DetectionJob{}, err
		}
	} else {
		// WithoutCancel keeps trace values but drops the caller's cancellation:
		// the inline job must survive the API client disconnecting, or Queue
		// would report a queued job that the disconnect silently killed.
		go s.Process(context.WithoutCancel(ctx), job)
	}
	return job, nil
}

func (s *Service) Process(ctx context.Context, job domain.DetectionJob) error {
	ctx, span := detectionTracer.Start(ctx, "detection.process",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			otelattr.String("webmonitoring.tenant_id", job.TenantID),
			otelattr.String("webmonitoring.job_id", job.ID),
			otelattr.String("webmonitoring.asset_id", job.AssetID),
			otelattr.String("webmonitoring.mode", job.Mode),
		))
	defer span.End()
	started := time.Now().UTC()
	claimed, err := s.repository.ClaimJob(ctx, job.TenantID, job.ID, started)
	if err != nil {
		s.logger.Error("claim detection job failed", "job_id", job.ID, "error", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "claim failed")
		return err
	}
	if !claimed {
		current, getErr := s.repository.GetJob(ctx, job.TenantID, job.ID)
		if getErr != nil {
			return fmt.Errorf("inspect unclaimed detection job: %w", getErr)
		}
		switch current.Status {
		case domain.JobSucceeded, domain.JobFailed, domain.JobTimedOut, domain.JobCancelled:
			return nil
		default:
			return fmt.Errorf("detection job %s is still %s", job.ID, current.Status)
		}
	}
	current, err := s.repository.GetJob(ctx, job.TenantID, job.ID)
	if err != nil {
		return fmt.Errorf("refresh claimed detection job: %w", err)
	}
	job = current
	job.Status = domain.JobRunning
	if s.metrics != nil {
		s.metrics.Inc("webmonitoring_detection_jobs_total", map[string]string{"mode": job.Mode, "status": "RUNNING"})
	}
	job.StartedAt = &started
	s.broker.Publish("detection.job.running", job)

	asset, err := s.repository.GetAsset(ctx, job.TenantID, job.AssetID)
	if err != nil {
		return s.failJob(ctx, job, "ASSET_NOT_FOUND", err)
	}
	policy, err := s.repository.GetPolicy(ctx, job.TenantID, job.AssetID)
	if err != nil {
		return s.failJob(ctx, job, "POLICY_NOT_FOUND", err)
	}
	if inMaintenanceWindow(policy, time.Now().UTC()) {
		return s.failJob(ctx, job, "MAINTENANCE_WINDOW", errors.New("detection suppressed during maintenance window"))
	}

	var result CheckResult
	if job.Mode == "BROWSER" {
		if s.browser == nil {
			return s.failJob(ctx, job, "BROWSER_UNAVAILABLE", errors.New("browser worker is not configured"))
		}
		result, err = s.browser.Check(ctx, asset, policy)
	} else {
		result, err = s.http.Check(ctx, asset, policy)
	}
	if err != nil {
		code := "CHECK_FAILED"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "CHECK_TIMEOUT"
		}
		return s.failJob(ctx, job, code, err)
	}

	evidenceIDs := make([]string, 0, 3)
	htmlEvidence, err := s.evidence.Put(asset.TenantID, asset.ID, job.ID, "page.html", "text/html", result.RawHTML)
	if err != nil {
		return s.failJob(ctx, job, "EVIDENCE_WRITE_FAILED", err)
	}
	retainUntil := htmlEvidence.CreatedAt.Add(s.evidenceRetention)
	htmlEvidence.RetainUntil = &retainUntil
	if err := s.repository.CreateEvidence(ctx, htmlEvidence); err != nil {
		return s.failJob(ctx, job, "EVIDENCE_METADATA_FAILED", err)
	}
	s.recordEvidenceWrite("page.html", "success")
	evidenceIDs = append(evidenceIDs, htmlEvidence.ID)

	snapshotJSON, _ := json.MarshalIndent(result.Snapshot, "", "  ")
	snapshotEvidence, err := s.evidence.Put(asset.TenantID, asset.ID, job.ID, "snapshot.json", "application/json", snapshotJSON)
	if err != nil {
		return s.failJob(ctx, job, "EVIDENCE_WRITE_FAILED", err)
	}
	snapshotRetainUntil := snapshotEvidence.CreatedAt.Add(s.evidenceRetention)
	snapshotEvidence.RetainUntil = &snapshotRetainUntil
	if err := s.repository.CreateEvidence(ctx, snapshotEvidence); err != nil {
		return s.failJob(ctx, job, "EVIDENCE_METADATA_FAILED", err)
	}
	s.recordEvidenceWrite("snapshot.json", "success")
	evidenceIDs = append(evidenceIDs, snapshotEvidence.ID)

	if len(result.Screenshot) > 0 {
		screenshotEvidence, putErr := s.evidence.Put(
			asset.TenantID, asset.ID, job.ID, "screenshot.png", "image/png", result.Screenshot,
		)
		if putErr != nil {
			return s.failJob(ctx, job, "EVIDENCE_WRITE_FAILED", putErr)
		}
		screenshotRetainUntil := screenshotEvidence.CreatedAt.Add(s.evidenceRetention)
		screenshotEvidence.RetainUntil = &screenshotRetainUntil
		if createErr := s.repository.CreateEvidence(ctx, screenshotEvidence); createErr != nil {
			return s.failJob(ctx, job, "EVIDENCE_METADATA_FAILED", createErr)
		}
		s.recordEvidenceWrite("screenshot.png", "success")
		evidenceIDs = append(evidenceIDs, screenshotEvidence.ID)
		result.Snapshot.ScreenshotHash = screenshotEvidence.SHA256
	}

	baseline, baselineErr := s.repository.GetApprovedBaseline(ctx, asset.TenantID, asset.ID)
	if errors.Is(baselineErr, store.ErrNotFound) {
		baselines, _, listErr := s.repository.ListBaselines(ctx, asset.TenantID, asset.ID, store.ListOptions{})
		if listErr != nil {
			return s.failJob(ctx, job, "BASELINE_READ_FAILED", listErr)
		}
		candidate := domain.BaselineVersion{
			ID:          identity.New(),
			TenantID:    asset.TenantID,
			AssetID:     asset.ID,
			Version:     nextBaselineVersion(baselines),
			Status:      domain.BaselineCandidate,
			Snapshot:    result.Snapshot,
			EvidenceIDs: evidenceIDs,
			CreatedBy:   "system",
			CreatedAt:   time.Now().UTC(),
		}
		if err := s.repository.CreateBaseline(ctx, candidate); err != nil {
			return s.failJob(ctx, job, "BASELINE_CREATE_FAILED", err)
		}
		s.broker.Publish("baseline.candidate.created", candidate)
	} else if baselineErr != nil {
		return s.failJob(ctx, job, "BASELINE_READ_FAILED", baselineErr)
	} else {
		job.BaselineVersion = baseline.Version
		signals := Compare(baseline.Snapshot, result.Snapshot, policy.TrustedDomains, evidenceIDs)
		if len(signals) > 0 {
			now := time.Now().UTC()
			severity := OverallSeverity(signals)
			incident := domain.Incident{
				ID:              identity.New(),
				TenantID:        asset.TenantID,
				AssetID:         asset.ID,
				BaselineID:      baseline.ID,
				BaselineVersion: baseline.Version,
				Fingerprint:     IncidentFingerprint(signals),
				Severity:        severity,
				Status:          domain.IncidentOpen,
				Title:           incidentTitle(asset, severity, signals),
				Signals:         signals,
				EvidenceIDs:     evidenceIDs,
				OccurrenceCount: 1,
				CreatedAt:       now,
				FirstSeenAt:     now,
				LastSeenAt:      now,
				// Carried into the timeline entry only; the store never writes
				// it to the incident row. It is what ties an occurrence to the
				// run that observed it.
				JobID: job.ID,
			}
			saved, outcome, upsertErr := s.repository.UpsertIncident(ctx, incident)
			if upsertErr != nil {
				return s.failJob(ctx, job, "INCIDENT_WRITE_FAILED", upsertErr)
			}
			if outcome == store.IncidentCreated {
				s.broker.Publish("incident.created", saved)
			} else {
				s.broker.Publish("incident.updated", saved)
			}
			if s.notifier != nil {
				// A recurrence notifies too. The original code notified only on
				// creation, which meant an incident that was seen once and then
				// independently raised again produced no second message -- the
				// operator who had already been paged was the one who most
				// needed to know it happened again. The reason travels with the
				// job so the message can say which of the three it is, and so the
				// delivery key keeps them apart.
				//
				// Reopened is a separate reason on purpose. A delivery's dedup
				// key is incidentID:channelID:reason, and a reopened incident
				// keeps its id, so reusing RECURRED would make this notice
				// collide with the recurrence notice already sent for that
				// incident and be dropped without anyone noticing.
				reason := domain.AlertReasonCreated
				switch outcome {
				case store.IncidentRecurred:
					reason = domain.AlertReasonRecurred
				case store.IncidentReopened:
					reason = domain.AlertReasonReopened
				}
				if s.alertQueue != nil {
					if err := s.alertQueue.EnqueueAlert(ctx, domain.AlertJob{
						TenantID: saved.TenantID, IncidentID: saved.ID, AssetID: saved.AssetID,
						Reason: reason,
					}); err != nil {
						return s.failJob(ctx, job, "ALERT_QUEUE_FAILED", err)
					}
				} else {
					for _, notifyErr := range s.notifier.SendIncident(ctx, saved, asset, reason) {
						s.logger.Warn("notification failed", "incident_id", saved.ID, "error", notifyErr)
					}
				}
			}
		}
	}

	finished := time.Now().UTC()
	// A cancel that lands while the check runs must win over a late success.
	if s.jobCancelled(ctx, job) {
		return nil
	}
	job.Status = domain.JobSucceeded
	job.FinishedAt = &finished
	if err := s.repository.UpdateJob(ctx, job); err != nil {
		return fmt.Errorf("persist successful detection job: %w", err)
	}
	s.broker.Publish("detection.job.succeeded", job)
	if s.metrics != nil {
		s.metrics.Inc("webmonitoring_detection_jobs_total", map[string]string{"mode": job.Mode, "status": string(job.Status)})
		s.observeJobDuration(job, finished)
	}
	return nil
}

// observeJobDuration records one histogram sample per terminal job outcome,
// measured from the attempt's claim time. Retry attempts only contribute the
// terminal outcome, so the count matches the job table's terminal rows.
func (s *Service) observeJobDuration(job domain.DetectionJob, finished time.Time) {
	if job.StartedAt == nil {
		return
	}
	s.metrics.ObserveDuration("webmonitoring_detection_job_duration_seconds", finished.Sub(*job.StartedAt), map[string]string{
		"mode":   job.Mode,
		"result": strings.ToLower(string(job.Status)),
	})
}

func (s *Service) failJob(ctx context.Context, job domain.DetectionJob, code string, err error) error {
	if s.jobCancelled(ctx, job) {
		return nil
	}
	if isRetryable(code) {
		maxAttempts := job.MaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = 3
		}
		if job.RetryCount+1 < maxAttempts {
			job.RetryCount++
			job.MaxAttempts = maxAttempts
			job.Status = domain.JobQueued
			job.ErrorCode = "RETRY_SCHEDULED"
			job.ErrorMessage = safeError(err)
			job.StartedAt = nil
			job.FinishedAt = nil
			if updateErr := s.repository.UpdateJob(ctx, job); updateErr != nil {
				s.logger.Error("persist detection retry failed", "job_id", job.ID, "error", updateErr)
				return updateErr
			} else {
				s.broker.Publish("detection.job.retrying", job)
				if s.metrics != nil {
					s.metrics.Inc("webmonitoring_detection_jobs_total", map[string]string{"mode": job.Mode, "status": "RETRYING", "error_code": code})
				}
				if s.queue != nil {
					if enqueueErr := s.enqueueRetry(ctx, job); enqueueErr != nil {
						return enqueueErr
					}
				} else {
					// Detach from the failing request: the retry timer and the
					// re-process must outlive the caller that just got an error.
					go s.enqueueRetry(context.WithoutCancel(ctx), job)
				}
				return nil
			}
		}
	}
	finished := time.Now().UTC()
	job.Status = domain.JobFailed
	if code == "CHECK_TIMEOUT" {
		job.Status = domain.JobTimedOut
	}
	job.ErrorCode = code
	job.ErrorMessage = safeError(err)
	job.FinishedAt = &finished
	if updateErr := s.repository.UpdateJob(ctx, job); updateErr != nil {
		s.logger.Error("persist detection failure failed", "job_id", job.ID, "error", updateErr)
		return updateErr
	}
	s.logger.Warn("detection job failed", "job_id", job.ID, "code", code, "error", err)
	if job.Status == domain.JobTimedOut {
		s.broker.Publish("detection.job.timed_out", job)
	} else {
		s.broker.Publish("detection.job.failed", job)
	}
	if s.metrics != nil {
		s.metrics.Inc("webmonitoring_detection_jobs_total", map[string]string{"mode": job.Mode, "status": string(job.Status), "error_code": code})
		s.observeJobDuration(job, finished)
		if strings.HasPrefix(code, "EVIDENCE_") {
			s.metrics.Inc("webmonitoring_evidence_failures_total", map[string]string{"error_code": code})
			s.metrics.Inc("webmonitoring_evidence_writes_total", map[string]string{"status": "failed"})
		}
	}
	return nil
}

// jobCancelled reports whether the persisted job has been cancelled since it
// was claimed. It is the best-effort guard that lets CANCELLED win over a late
// SUCCEEDED/FAILED write from a check that was already in flight.
func (s *Service) jobCancelled(ctx context.Context, job domain.DetectionJob) bool {
	current, err := s.repository.GetJob(ctx, job.TenantID, job.ID)
	if err != nil {
		return false
	}
	return current.Status == domain.JobCancelled
}

func (s *Service) enqueueRetry(ctx context.Context, job domain.DetectionJob) error {
	delay := time.Duration(1<<job.RetryCount) * s.retryBase
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	if s.queue != nil {
		if err := s.queue.Enqueue(ctx, job); err != nil {
			return fmt.Errorf("enqueue detection retry: %w", err)
		}
		return nil
	}
	// The retry already outlived its request via WithoutCancel upstream; keep
	// the same detached context for the re-process it triggers.
	go func() {
		if err := s.Process(context.WithoutCancel(ctx), job); err != nil {
			s.logger.Error("process embedded detection retry failed", "job_id", job.ID, "error", err)
		}
	}()
	return nil
}

func (s *Service) recordEvidenceWrite(kind, status string) {
	if s.metrics != nil {
		s.metrics.Inc("webmonitoring_evidence_writes_total", map[string]string{
			"kind": kind, "status": status,
		})
	}
}

func isRetryable(code string) bool {
	switch code {
	case "CHECK_FAILED", "EVIDENCE_WRITE_FAILED", "EVIDENCE_METADATA_FAILED",
		"BASELINE_READ_FAILED", "BASELINE_CREATE_FAILED", "INCIDENT_WRITE_FAILED", "ALERT_QUEUE_FAILED":
		return true
	default:
		return false
	}
}

func nextBaselineVersion(baselines []domain.BaselineVersion) int {
	maximum := 0
	for _, baseline := range baselines {
		if baseline.Version > maximum {
			maximum = baseline.Version
		}
	}
	return maximum + 1
}

func inMaintenanceWindow(policy domain.MonitorPolicy, now time.Time) bool {
	return policy.MaintenanceWindowStart != nil && policy.MaintenanceWindowEnd != nil &&
		!now.Before(*policy.MaintenanceWindowStart) && now.Before(*policy.MaintenanceWindowEnd)
}

func incidentTitle(asset domain.Asset, severity domain.Severity, signals []domain.Signal) string {
	return fmt.Sprintf("%s 检测到 %s 风险：%s", asset.Name, severity, signals[0].Summary)
}

func safeError(err error) string {
	message := security.RedactString(err.Error())
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}
