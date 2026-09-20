package detection

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/store"
)

type Scheduler struct {
	repository store.Repository
	service    *Service
	logger     *slog.Logger
}

func NewScheduler(repository store.Repository, service *Service, logger *slog.Logger) *Scheduler {
	return &Scheduler{repository: repository, service: service, logger: logger}
}

func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	s.scheduleAll()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scheduleAll()
		}
	}
}

func (s *Scheduler) scheduleAll() {
	tenants, _, err := s.repository.ListTenants(store.ListOptions{})
	if err != nil {
		s.logger.Error("list tenants for scheduling", "error", err)
		return
	}
	for _, tenant := range tenants {
		s.schedule(tenant.ID)
	}
}

func (s *Scheduler) schedule(tenantID string) {
	now := time.Now().UTC()
	// The lease comes from the repository rather than the package constant, so
	// the reap cutoff and the ClaimJob reclaim cutoff cannot drift apart.
	if reaped, err := s.repository.ReapTimedOut(tenantID, now.Add(-s.repository.JobLease())); err != nil {
		s.logger.Error("reap timed out detection jobs", "error", err)
	} else if reaped > 0 {
		s.logger.Warn("reaped stale RUNNING detection jobs", "tenant_id", tenantID, "count", reaped)
	}
	jobs, _, err := s.repository.ListJobs(tenantID, store.ListOptions{})
	if err != nil {
		s.logger.Error("list scheduled jobs", "error", err)
		return
	}
	assets, _, err := s.repository.ListAssets(tenantID, store.ListOptions{})
	if err != nil {
		s.logger.Error("list scheduled assets", "error", err)
		return
	}
	for _, asset := range assets {
		if !asset.Enabled || asset.VerificationStatus != domain.VerificationVerified {
			continue
		}
		policy, err := s.repository.GetPolicy(tenantID, asset.ID)
		if err != nil || inMaintenanceWindow(policy, now) {
			continue
		}
		s.queueIfDue(tenantID, asset.ID, "HTTP", policy.HTTPIntervalSeconds, jobs, now)
		s.queueIfDue(tenantID, asset.ID, "BROWSER", policy.BrowserIntervalSeconds, jobs, now)
	}
}

// queueIfDue enqueues a check when its interval has elapsed.
//
// ErrJobAlreadyActive is expected here rather than exceptional. due() reads the
// job table and the insert happens afterwards, so a second control-plane replica
// can queue the same asset in between; the partial unique index from migration
// 000012 rejects the loser. Logging that at warn on every 30s tick would bury
// real scheduling failures, so it is a silent no-op.
func (s *Scheduler) queueIfDue(tenantID, assetID, mode string, intervalSeconds int, jobs []domain.DetectionJob, now time.Time) {
	if !due(assetID, mode, intervalSeconds, jobs, now) {
		return
	}
	if _, err := s.service.Queue(tenantID, assetID, mode); err != nil &&
		!errors.Is(err, store.ErrJobAlreadyActive) {
		s.logger.Warn("schedule detection check", "asset_id", assetID, "mode", mode, "error", err)
	}
}

func due(assetID, mode string, intervalSeconds int, jobs []domain.DetectionJob, now time.Time) bool {
	if intervalSeconds <= 0 {
		return false
	}
	var latest time.Time
	for _, job := range jobs {
		if job.AssetID == assetID && job.Mode == mode && job.CreatedAt.After(latest) {
			latest = job.CreatedAt
		}
		if job.AssetID == assetID && job.Mode == mode && job.Status.IsActive() {
			return false
		}
	}
	return latest.IsZero() || now.Sub(latest) >= time.Duration(intervalSeconds)*time.Second
}
