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
	"webmonitoring/internal/notify"
	"webmonitoring/internal/store"
)

type BrowserChecker interface {
	Check(context.Context, domain.Asset, domain.MonitorPolicy) (CheckResult, error)
}

type JobQueue interface {
	Enqueue(context.Context, domain.DetectionJob) error
}

type Service struct {
	repository store.Repository
	evidence   evidence.Store
	http       *Checker
	browser    BrowserChecker
	notifier   *notify.Dispatcher
	broker     *events.Broker
	logger     *slog.Logger
	queue      JobQueue
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
		repository: repository,
		evidence:   evidenceStore,
		http:       httpChecker,
		browser:    browserChecker,
		notifier:   notifier,
		broker:     broker,
		logger:     logger,
	}
}

func (s *Service) SetQueue(queue JobQueue) {
	s.queue = queue
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
		CreatedAt:     time.Now().UTC(),
	}
	if err := s.repository.CreateJob(job); err != nil {
		return domain.DetectionJob{}, err
	}
	s.broker.Publish("detection.job.queued", job)
	if s.queue != nil {
		if err := s.queue.Enqueue(context.Background(), job); err != nil {
			s.failJob(job, "QUEUE_FAILED", err)
			return domain.DetectionJob{}, err
		}
	} else {
		go s.Process(context.Background(), job)
	}
	return job, nil
}

func (s *Service) Process(ctx context.Context, job domain.DetectionJob) {
	current, err := s.repository.GetJob(job.TenantID, job.ID)
	if err == nil && (current.Status == domain.JobSucceeded || current.Status == domain.JobCancelled) {
		return
	}
	started := time.Now().UTC()
	job.Status = domain.JobRunning
	job.StartedAt = &started
	_ = s.repository.UpdateJob(job)
	s.broker.Publish("detection.job.running", job)

	asset, err := s.repository.GetAsset(job.TenantID, job.AssetID)
	if err != nil {
		s.failJob(job, "ASSET_NOT_FOUND", err)
		return
	}
	policy, err := s.repository.GetPolicy(job.TenantID, job.AssetID)
	if err != nil {
		s.failJob(job, "POLICY_NOT_FOUND", err)
		return
	}
	if inMaintenanceWindow(policy, time.Now().UTC()) {
		s.failJob(job, "MAINTENANCE_WINDOW", errors.New("detection suppressed during maintenance window"))
		return
	}

	var result CheckResult
	if job.Mode == "BROWSER" {
		if s.browser == nil {
			s.failJob(job, "BROWSER_UNAVAILABLE", errors.New("browser worker is not configured"))
			return
		}
		result, err = s.browser.Check(ctx, asset, policy)
	} else {
		result, err = s.http.Check(ctx, asset, policy)
	}
	if err != nil {
		s.failJob(job, "CHECK_FAILED", err)
		return
	}

	evidenceIDs := make([]string, 0, 3)
	htmlEvidence, err := s.evidence.Put(asset.TenantID, asset.ID, job.ID, "page.html", "text/html", result.RawHTML)
	if err != nil {
		s.failJob(job, "EVIDENCE_WRITE_FAILED", err)
		return
	}
	if err := s.repository.CreateEvidence(htmlEvidence); err != nil {
		s.failJob(job, "EVIDENCE_METADATA_FAILED", err)
		return
	}
	evidenceIDs = append(evidenceIDs, htmlEvidence.ID)

	snapshotJSON, _ := json.MarshalIndent(result.Snapshot, "", "  ")
	snapshotEvidence, err := s.evidence.Put(asset.TenantID, asset.ID, job.ID, "snapshot.json", "application/json", snapshotJSON)
	if err != nil {
		s.failJob(job, "EVIDENCE_WRITE_FAILED", err)
		return
	}
	if err := s.repository.CreateEvidence(snapshotEvidence); err != nil {
		s.failJob(job, "EVIDENCE_METADATA_FAILED", err)
		return
	}
	evidenceIDs = append(evidenceIDs, snapshotEvidence.ID)

	if len(result.Screenshot) > 0 {
		screenshotEvidence, putErr := s.evidence.Put(
			asset.TenantID, asset.ID, job.ID, "screenshot.png", "image/png", result.Screenshot,
		)
		if putErr != nil {
			s.failJob(job, "EVIDENCE_WRITE_FAILED", putErr)
			return
		}
		if createErr := s.repository.CreateEvidence(screenshotEvidence); createErr != nil {
			s.failJob(job, "EVIDENCE_METADATA_FAILED", createErr)
			return
		}
		evidenceIDs = append(evidenceIDs, screenshotEvidence.ID)
		result.Snapshot.ScreenshotHash = screenshotEvidence.SHA256
	}

	baseline, baselineErr := s.repository.GetApprovedBaseline(asset.TenantID, asset.ID)
	if errors.Is(baselineErr, store.ErrNotFound) {
		candidate := domain.BaselineVersion{
			ID:          identity.New(),
			TenantID:    asset.TenantID,
			AssetID:     asset.ID,
			Version:     nextBaselineVersion(s.repository.ListBaselines(asset.TenantID, asset.ID)),
			Status:      domain.BaselineCandidate,
			Snapshot:    result.Snapshot,
			EvidenceIDs: evidenceIDs,
			CreatedBy:   "system",
			CreatedAt:   time.Now().UTC(),
		}
		if err := s.repository.CreateBaseline(candidate); err != nil {
			s.failJob(job, "BASELINE_CREATE_FAILED", err)
			return
		}
		s.broker.Publish("baseline.candidate.created", candidate)
	} else if baselineErr != nil {
		s.failJob(job, "BASELINE_READ_FAILED", baselineErr)
		return
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
			}
			saved, created, upsertErr := s.repository.UpsertIncident(incident)
			if upsertErr != nil {
				s.failJob(job, "INCIDENT_WRITE_FAILED", upsertErr)
				return
			}
			if created {
				s.broker.Publish("incident.created", saved)
				for _, notifyErr := range s.notifier.SendIncident(ctx, saved, asset) {
					s.logger.Warn("notification failed", "incident_id", saved.ID, "error", notifyErr)
				}
			} else {
				s.broker.Publish("incident.updated", saved)
			}
		}
	}

	finished := time.Now().UTC()
	job.Status = domain.JobSucceeded
	job.FinishedAt = &finished
	_ = s.repository.UpdateJob(job)
	s.broker.Publish("detection.job.succeeded", job)
}

func (s *Service) failJob(job domain.DetectionJob, code string, err error) {
	finished := time.Now().UTC()
	job.Status = domain.JobFailed
	job.ErrorCode = code
	job.ErrorMessage = safeError(err)
	job.FinishedAt = &finished
	_ = s.repository.UpdateJob(job)
	s.logger.Warn("detection job failed", "job_id", job.ID, "code", code, "error", err)
	s.broker.Publish("detection.job.failed", job)
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
	message := err.Error()
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}
