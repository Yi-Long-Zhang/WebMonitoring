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
	s.scheduleAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scheduleAll(ctx)
		}
	}
}

func (s *Scheduler) scheduleAll(ctx context.Context) {
	tenants, _, err := s.repository.ListTenants(ctx, store.ListOptions{})
	if err != nil {
		s.logger.Error("list tenants for scheduling", "error", err)
		return
	}
	for _, tenant := range tenants {
		s.schedule(ctx, tenant.ID)
	}
}

func (s *Scheduler) schedule(ctx context.Context, tenantID string) {
	now := time.Now().UTC()
	// The lease comes from the repository rather than the package constant, so
	// the reap cutoff and the ClaimJob reclaim cutoff cannot drift apart.
	if reaped, err := s.repository.ReapTimedOut(ctx, tenantID, now.Add(-s.repository.JobLease())); err != nil {
		s.logger.Error("reap timed out detection jobs", "error", err)
	} else if reaped > 0 {
		s.logger.Warn("reaped stale RUNNING detection jobs", "tenant_id", tenantID, "count", reaped)
	}
	// JobSchedulingState collapses the whole job history into one row per
	// (asset, mode), so the 30s tick costs one aggregate query no matter how
	// many jobs have accumulated. Without it, every tick hauled the full table
	// into memory and grew linearly with history -- weeks of jobs meant the
	// scheduler itself became the OOM source it was meant to prevent.
	activity, err := s.repository.JobSchedulingState(ctx, tenantID)
	if err != nil {
		s.logger.Error("list scheduled jobs", "error", err)
		return
	}
	state := make(map[string]store.JobActivity, len(activity))
	for _, item := range activity {
		state[item.AssetID+"/"+item.Mode] = item
	}
	// Policies load once and index by asset, replacing a per-asset GetPolicy
	// round trip (N+1) with one list. An asset with no policy row has nothing
	// to schedule, exactly as before.
	policies, _, err := s.repository.ListPolicies(ctx, tenantID, store.ListOptions{})
	if err != nil {
		s.logger.Error("list scheduled policies", "error", err)
		return
	}
	policyByAsset := make(map[string]domain.MonitorPolicy, len(policies))
	for _, policy := range policies {
		policyByAsset[policy.AssetID] = policy
	}
	assets, _, err := s.repository.ListAssets(ctx, tenantID, store.ListOptions{})
	if err != nil {
		s.logger.Error("list scheduled assets", "error", err)
		return
	}
	for _, asset := range assets {
		if !asset.Enabled || asset.VerificationStatus != domain.VerificationVerified {
			continue
		}
		policy, ok := policyByAsset[asset.ID]
		if !ok || inMaintenanceWindow(policy, now) {
			continue
		}
		item, _ := state[asset.ID+"/HTTP"]
		s.queueIfDue(ctx, tenantID, asset.ID, "HTTP", policy.HTTPIntervalSeconds, item, now)
		item, _ = state[asset.ID+"/BROWSER"]
		s.queueIfDue(ctx, tenantID, asset.ID, "BROWSER", policy.BrowserIntervalSeconds, item, now)
	}
}

// queueIfDue enqueues a check when its interval has elapsed.
//
// ErrJobAlreadyActive is expected here rather than exceptional. due() reads the
// job table and the insert happens afterwards, so a second control-plane replica
// can queue the same asset in between; the partial unique index from migration
// 000012 rejects the loser. Logging that at warn on every 30s tick would bury
// real scheduling failures, so it is a silent no-op.
func (s *Scheduler) queueIfDue(ctx context.Context, tenantID, assetID, mode string, intervalSeconds int, item store.JobActivity, now time.Time) {
	if !due(intervalSeconds, item, now) {
		return
	}
	if _, err := s.service.Queue(ctx, tenantID, assetID, mode); err != nil &&
		!errors.Is(err, store.ErrJobAlreadyActive) {
		s.logger.Warn("schedule detection check", "asset_id", assetID, "mode", mode, "error", err)
	}
}

// due decides whether a check is due from the (asset, mode) aggregate alone:
// an active job blocks regardless of age, otherwise the interval must have
// elapsed since the newest job. A pair with no rows at all (zero item) is
// always due -- the asset has never been checked.
func due(intervalSeconds int, item store.JobActivity, now time.Time) bool {
	if intervalSeconds <= 0 {
		return false
	}
	if item.HasActive {
		return false
	}
	return item.LatestCreatedAt.IsZero() || now.Sub(item.LatestCreatedAt) >= time.Duration(intervalSeconds)*time.Second
}
