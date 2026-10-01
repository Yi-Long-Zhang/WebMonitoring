package detection

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/events"
	"webmonitoring/internal/store"
)

type capacityQueue struct {
	mu   sync.Mutex
	jobs []domain.DetectionJob
}

func (q *capacityQueue) Enqueue(_ context.Context, job domain.DetectionJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, job)
	return nil
}

func TestSchedulerQueues100AssetsWithoutDuplicateJobs(t *testing.T) {
	repository := store.NewMemoryRepository()
	now := time.Now().UTC()
	for index := 0; index < 100; index++ {
		assetID := "asset-" + string(rune('a'+index/26)) + string(rune('a'+index%26))
		if err := repository.CreateAsset(context.Background(), domain.Asset{
			ID: assetID, TenantID: "tenant", Name: assetID, URL: "https://example.com/" + assetID,
			OwnerID: "owner", Enabled: true, VerificationStatus: domain.VerificationVerified,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := repository.UpsertPolicy(context.Background(), domain.MonitorPolicy{
			ID: "policy-" + assetID, TenantID: "tenant", AssetID: assetID, Version: 1,
			HTTPIntervalSeconds: 300, BrowserIntervalSeconds: 300,
			TimeoutSeconds: 30, MaxResponseBytes: 5 * 1024 * 1024, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	queue := &capacityQueue{}
	service := NewService(repository, nil, nil, nil, nil, events.NewBroker(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.SetQueue(queue)
	scheduler := NewScheduler(repository, service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	scheduler.schedule(context.Background(), "tenant")
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.jobs) != 200 {
		t.Fatalf("queued jobs=%d, want 200", len(queue.jobs))
	}
	seen := make(map[string]struct{}, len(queue.jobs))
	for _, job := range queue.jobs {
		key := job.AssetID + ":" + job.Mode
		if _, ok := seen[key]; ok {
			t.Fatalf("duplicate scheduled job %s", key)
		}
		seen[key] = struct{}{}
	}
}
