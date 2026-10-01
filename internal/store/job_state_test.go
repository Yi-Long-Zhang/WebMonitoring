package store

import (
	"context"
	"testing"
	"time"

	"webmonitoring/internal/domain"
)

func TestCancelJobOnlyTerminatesQueuedOrRunning(t *testing.T) {
	repository := NewMemoryRepository()
	now := time.Now().UTC()
	// Each job gets its own asset because migration 000012 allows at most one
	// QUEUED or RUNNING job per (tenant, asset, mode); two active jobs on the
	// same asset is no longer a reachable state.
	for _, job := range []domain.DetectionJob{
		{ID: "queued", TenantID: "tenant", AssetID: "asset-queued", Mode: "HTTP", Status: domain.JobQueued, CreatedAt: now},
		{ID: "running", TenantID: "tenant", AssetID: "asset-running", Mode: "HTTP", Status: domain.JobRunning, CreatedAt: now, StartedAt: &now},
		{ID: "succeeded", TenantID: "tenant", AssetID: "asset-succeeded", Mode: "HTTP", Status: domain.JobSucceeded, CreatedAt: now},
	} {
		if err := repository.CreateJob(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"queued", "running"} {
		changed, err := repository.CancelJob(context.Background(), "tenant", id)
		if err != nil || !changed {
			t.Fatalf("cancel %s: changed=%v err=%v", id, changed, err)
		}
		if job, _ := repository.GetJob(context.Background(), "tenant", id); job.Status != domain.JobCancelled {
			t.Fatalf("job %s status=%s, want CANCELLED", id, job.Status)
		}
	}
	changed, err := repository.CancelJob(context.Background(), "tenant", "succeeded")
	if err != nil || changed {
		t.Fatalf("cancel succeeded: changed=%v err=%v", changed, err)
	}
	if job, _ := repository.GetJob(context.Background(), "tenant", "succeeded"); job.Status != domain.JobSucceeded {
		t.Fatalf("succeeded job mutated to %s", job.Status)
	}
}

func TestReapTimedOutOnlyReapsStaleRunning(t *testing.T) {
	repository := NewMemoryRepository()
	now := time.Now().UTC()
	staleStart := now.Add(-10 * time.Minute)
	freshStart := now.Add(-time.Minute)
	// Distinct assets: two RUNNING jobs and a QUEUED job cannot share one asset
	// under the active-job index from migration 000012.
	for _, job := range []domain.DetectionJob{
		{ID: "stale", TenantID: "tenant", AssetID: "asset-stale", Mode: "HTTP", Status: domain.JobRunning, CreatedAt: now, StartedAt: &staleStart},
		{ID: "fresh", TenantID: "tenant", AssetID: "asset-fresh", Mode: "HTTP", Status: domain.JobRunning, CreatedAt: now, StartedAt: &freshStart},
		{ID: "queued", TenantID: "tenant", AssetID: "asset-queued", Mode: "HTTP", Status: domain.JobQueued, CreatedAt: now},
	} {
		if err := repository.CreateJob(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	reaped, err := repository.ReapTimedOut(context.Background(), "tenant", now.Add(-JobLease))
	if err != nil || reaped != 1 {
		t.Fatalf("reaped=%d err=%v", reaped, err)
	}
	if job, _ := repository.GetJob(context.Background(), "tenant", "stale"); job.Status != domain.JobTimedOut {
		t.Fatalf("stale job status=%s, want TIMED_OUT", job.Status)
	}
	if job, _ := repository.GetJob(context.Background(), "tenant", "fresh"); job.Status != domain.JobRunning {
		t.Fatalf("fresh job status=%s, want RUNNING", job.Status)
	}
	if job, _ := repository.GetJob(context.Background(), "tenant", "queued"); job.Status != domain.JobQueued {
		t.Fatalf("queued job status=%s, want QUEUED", job.Status)
	}
}
