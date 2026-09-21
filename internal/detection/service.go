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
	asset, err := s.repository.GetAsset(incident.TenantID, incident.AssetID)
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

func (s *Service) Queue(tenantID, assetID, mode string) (domain.DetectionJob, error) {
	asset, err := s.repository.GetAsset(tenantID, assetID)
	if err != nil {
		return domain.DetectionJob{}, err
	}
	if !asset.Enabled || asset.VerificationStatus != domain.VerificationVerified {
		return domain.DetectionJob{}, errors.New("asset must be enabled and verified before detection")
	}
	policy, err := s.repository.GetPolicy(tenantID, assetID)
	if err != nil {
		return domain.DetectionJob{}, errors.New("asset has no monitoring policy")
	}
	mode = strings.ToUpper(mode)
	if mode == "" {
		mode = "HTTP"
	}
	if mode != "HTTP" && mode != "BROWSER" {
		return domain.DetectionJob{}, errors.New("mode must be HTTP or BROWSER")
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
	if err := s.repository.CreateJob(job); err != nil {
		return domain.DetectionJob{}, err
	}
	if s.metrics != nil {
		s.metrics.Inc("webmonitoring_detection_jobs_total", map[string]string{"mode": mode, "status": "QUEUED"})
	}
	s.broker.Publish("detection.job.queued", job)
	if s.queue != nil {
		if err := s.queue.Enqueue(context.Background(), job); err != nil {
			_ = s.failJob(context.Background(), job, "QUEUE_FAILED", err)
			return domain.DetectionJob{}, err
		}
	} else {
		go s.Process(context.Background(), job)
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
	claimed, err := s.repository.ClaimJob(job.TenantID, job.ID, started)
	if err != nil {
		s.logger.Error("claim detection job failed", "job_id", job.ID, "error", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "claim failed")
		return err
	}
	if !claimed {
		current, getErr := s.repository.GetJob(job.TenantID, job.ID)
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
	current, err := s.repository.GetJob(job.TenantID, job.ID)
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

	asset, err := s.repository.GetAsset(job.TenantID, job.AssetID)
	if err != nil {
		return s.failJob(ctx, job, "ASSET_NOT_FOUND", err)
	}
	policy, err := s.repository.GetPolicy(job.TenantID, job.AssetID)
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
	if err := s.repository.CreateEvidence(htmlEvidence); err != nil {
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
	if err := s.repository.CreateEvidence(snapshotEvidence); err != nil {
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
		if createErr := s.repository.CreateEvidence(screenshotEvidence); createErr != nil {
			return s.failJob(ctx, job, "EVIDENCE_METADATA_FAILED", createErr)
		}
		s.recordEvidenceWrite("screenshot.png", "success")
		evidenceIDs = append(evidenceIDs, screenshotEvidence.ID)
		result.Snapshot.ScreenshotHash = screenshotEvidence.SHA256
	}

	baseline, baselineErr := s.repository.GetApprovedBaseline(asset.TenantID, asset.ID)
	if errors.Is(baselineErr, store.ErrNotFound) {
		baselines, _, listErr := s.repository.ListBaselines(asset.TenantID, asset.ID, store.ListOptions{})
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
		if err := s.repository.CreateBaseline(candidate); err != nil {
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
			saved, created, upsertErr := s.repository.UpsertIncident(incident)
			if upsertErr != nil {
				return s.failJob(ctx, job, "INCIDENT_WRITE_FAILED", upsertErr)
			}
			if created {
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
				// job so the message can say which of the two it is, and so the
				// delivery key keeps them apart.
				reason := domain.AlertReasonCreated
				if !created {
					reason = domain.AlertReasonRecurred
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
	if s.jobCancelled(job) {
		return nil
	}
	job.Status = domain.JobSucceeded
	job.FinishedAt = &finished
	if err := s.repository.UpdateJob(job); err != nil {
		return fmt.Errorf("persist successful detection job: %w", err)
	}
	s.broker.Publish("detection.job.succeeded", job)
	if s.metrics != nil {
		s.metrics.Inc("webmonitoring_detection_jobs_total", map[string]string{"mode": job.Mode, "status": string(job.Status)})
	}
	return nil
}

func (s *Service) failJob(ctx context.Context, job domain.DetectionJob, code string, err error) error {
	if s.jobCancelled(job) {
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
			if updateErr := s.repository.UpdateJob(job); updateErr != nil {
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
					go s.enqueueRetry(context.Background(), job)
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
	if updateErr := s.repository.UpdateJob(job); updateErr != nil {
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
func (s *Service) jobCancelled(job domain.DetectionJob) bool {
	current, err := s.repository.GetJob(job.TenantID, job.ID)
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
	go func() {
		if err := s.Process(context.Background(), job); err != nil {
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
