package detection

import (
	"context"
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
	if reaped, err := s.repository.ReapTimedOut(tenantID, now.Add(-store.JobLease)); err != nil {
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
		if due(asset.ID, "HTTP", policy.HTTPIntervalSeconds, jobs, now) {
			if _, err := s.service.Queue(tenantID, asset.ID, "HTTP"); err != nil {
				s.logger.Warn("schedule HTTP check", "asset_id", asset.ID, "error", err)
			}
		}
		if due(asset.ID, "BROWSER", policy.BrowserIntervalSeconds, jobs, now) {
			if _, err := s.service.Queue(tenantID, asset.ID, "BROWSER"); err != nil {
				s.logger.Warn("schedule browser check", "asset_id", asset.ID, "error", err)
			}
		}
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
		if job.AssetID == assetID && job.Mode == mode &&
			(job.Status == domain.JobQueued || job.Status == domain.JobRunning) {
			return false
		}
	}
	return latest.IsZero() || now.Sub(latest) >= time.Duration(intervalSeconds)*time.Second
}
