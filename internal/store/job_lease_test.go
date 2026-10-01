package store

import (
	"context"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
)

// ReapTimedOut does not check which replica owns a job: it moves any RUNNING job
// whose started_at is older than the cutoff to TIMED_OUT. The lease is therefore
// the entire guarantee that a slow detection is not taken away from the worker
// still running it, which makes the boundary the thing worth pinning -- and
// pinning it in both implementations, because the reclaim logic lives in each.
func TestJobLeaseGovernsTheReapBoundary(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			tenantID, assetID := seedJobFixture(t, repository)
			repository.SetJobLease(time.Minute)
			if got := repository.JobLease(); got != time.Minute {
				t.Fatalf("JobLease() = %s, want 1m", got)
			}
			now := time.Now().UTC()

			// Both jobs share one asset and differ by mode: migration 000012
			// allows one active job per (tenant, asset, mode), so this is the
			// only way to hold two RUNNING jobs at once.
			young := now.Add(-10 * time.Second)
			old := now.Add(-2 * time.Minute)
			// detection_jobs.id is a uuid and CreateJob binds it without a cast,
			// so the ids have to be real uuids: a readable literal passes in
			// memory and fails on Postgres with 22P02.
			youngID, oldID := identity.New(), identity.New()
			for _, job := range []domain.DetectionJob{
				{ID: youngID, TenantID: tenantID, AssetID: assetID, Mode: "HTTP", Status: domain.JobRunning, CreatedAt: now, StartedAt: &young, MaxAttempts: 3},
				{ID: oldID, TenantID: tenantID, AssetID: assetID, Mode: "BROWSER", Status: domain.JobRunning, CreatedAt: now, StartedAt: &old, MaxAttempts: 3},
			} {
				if err := repository.CreateJob(context.Background(), job); err != nil {
					t.Fatal(err)
				}
			}

			reaped, err := repository.ReapTimedOut(context.Background(), tenantID, now.Add(-repository.JobLease()))
			if err != nil {
				t.Fatal(err)
			}
			if reaped != 1 {
				t.Fatalf("reaped=%d, want only the job older than the lease", reaped)
			}
			if job, _ := repository.GetJob(context.Background(), tenantID, youngID); job.Status != domain.JobRunning {
				t.Fatalf("a job 10s into a 60s lease was reaped: status=%s", job.Status)
			}
			if job, _ := repository.GetJob(context.Background(), tenantID, oldID); job.Status != domain.JobTimedOut {
				t.Fatalf("a job 120s into a 60s lease was not reaped: status=%s", job.Status)
			}
		})
	}
}

// ClaimJob reclaims a stale RUNNING claim so a crashed worker's job can be
// retried. It has to use the same lease as the reaper: if it used a different
// one, a job could be reclaimed while still legitimately running, or never
// reclaimed at all.
func TestClaimJobReclaimsOnlyPastTheLease(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			tenantID, assetID := seedJobFixture(t, repository)
			repository.SetJobLease(time.Minute)
			now := time.Now().UTC()
			recent := now.Add(-10 * time.Second)
			jobID := identity.New()
			if err := repository.CreateJob(context.Background(), domain.DetectionJob{
				ID: jobID, TenantID: tenantID, AssetID: assetID, Mode: "HTTP",
				Status: domain.JobRunning, CreatedAt: now, StartedAt: &recent, MaxAttempts: 3,
			}); err != nil {
				t.Fatal(err)
			}

			claimed, err := repository.ClaimJob(context.Background(), tenantID, jobID, now)
			if err != nil {
				t.Fatal(err)
			}
			if claimed {
				t.Fatal("a job 10s into a 60s lease was reclaimed from its worker")
			}
		})
	}
}

// A non-positive lease would make every claim instantly reclaimable and every
// running job immediately reaped, so a misread environment variable must not be
// able to set one.
func TestSetJobLeaseIgnoresNonPositiveValues(t *testing.T) {
	repository := NewMemoryRepository()
	repository.SetJobLease(0)
	if repository.JobLease() != JobLease {
		t.Fatalf("a zero lease replaced the default: %s", repository.JobLease())
	}
	repository.SetJobLease(-time.Minute)
	if repository.JobLease() != JobLease {
		t.Fatalf("a negative lease replaced the default: %s", repository.JobLease())
	}
}
