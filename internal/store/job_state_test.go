package store

import (
	"testing"
	"time"

	"webmonitoring/internal/domain"
)

func TestCancelJobOnlyTerminatesQueuedOrRunning(t *testing.T) {
	repository := NewMemoryRepository()
	now := time.Now().UTC()
	for _, job := range []domain.DetectionJob{
		{ID: "queued", TenantID: "tenant", AssetID: "asset", Mode: "HTTP", Status: domain.JobQueued, CreatedAt: now},
		{ID: "running", TenantID: "tenant", AssetID: "asset", Mode: "HTTP", Status: domain.JobRunning, CreatedAt: now, StartedAt: &now},
		{ID: "succeeded", TenantID: "tenant", AssetID: "asset", Mode: "HTTP", Status: domain.JobSucceeded, CreatedAt: now},
	} {
		if err := repository.CreateJob(job); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"queued", "running"} {
		changed, err := repository.CancelJob("tenant", id)
		if err != nil || !changed {
			t.Fatalf("cancel %s: changed=%v err=%v", id, changed, err)
		}
		if job, _ := repository.GetJob("tenant", id); job.Status != domain.JobCancelled {
			t.Fatalf("job %s status=%s, want CANCELLED", id, job.Status)
		}
	}
	changed, err := repository.CancelJob("tenant", "succeeded")
	if err != nil || changed {
		t.Fatalf("cancel succeeded: changed=%v err=%v", changed, err)
	}
	if job, _ := repository.GetJob("tenant", "succeeded"); job.Status != domain.JobSucceeded {
		t.Fatalf("succeeded job mutated to %s", job.Status)
	}
}

func TestReapTimedOutOnlyReapsStaleRunning(t *testing.T) {
	repository := NewMemoryRepository()
	now := time.Now().UTC()
	staleStart := now.Add(-10 * time.Minute)
	freshStart := now.Add(-time.Minute)
	for _, job := range []domain.DetectionJob{
		{ID: "stale", TenantID: "tenant", AssetID: "asset", Mode: "HTTP", Status: domain.JobRunning, CreatedAt: now, StartedAt: &staleStart},
		{ID: "fresh", TenantID: "tenant", AssetID: "asset", Mode: "HTTP", Status: domain.JobRunning, CreatedAt: now, StartedAt: &freshStart},
		{ID: "queued", TenantID: "tenant", AssetID: "asset", Mode: "HTTP", Status: domain.JobQueued, CreatedAt: now},
	} {
		if err := repository.CreateJob(job); err != nil {
			t.Fatal(err)
		}
	}
	reaped, err := repository.ReapTimedOut("tenant", now.Add(-JobLease))
	if err != nil || reaped != 1 {
		t.Fatalf("reaped=%d err=%v", reaped, err)
	}
	if job, _ := repository.GetJob("tenant", "stale"); job.Status != domain.JobTimedOut {
		t.Fatalf("stale job status=%s, want TIMED_OUT", job.Status)
	}
	if job, _ := repository.GetJob("tenant", "fresh"); job.Status != domain.JobRunning {
		t.Fatalf("fresh job status=%s, want RUNNING", job.Status)
	}
	if job, _ := repository.GetJob("tenant", "queued"); job.Status != domain.JobQueued {
		t.Fatalf("queued job status=%s, want QUEUED", job.Status)
	}
}
