package detection

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/events"
	"webmonitoring/internal/store"
)

type checkingQueue struct {
	repository store.Repository
	called     bool
}

func (q *checkingQueue) Enqueue(_ context.Context, job domain.DetectionJob) error {
	persisted, err := q.repository.GetJob(job.TenantID, job.ID)
	if err != nil {
		return err
	}
	if persisted.Status != domain.JobQueued || persisted.RetryCount != 1 {
		return errors.New("retry was enqueued before its state was persisted")
	}
	q.called = true
	return nil
}

func TestRetryIsPersistedBeforeQueueAcknowledgement(t *testing.T) {
	repository := store.NewMemoryRepository()
	job := domain.DetectionJob{
		ID: "job", TenantID: "tenant", AssetID: "asset", Mode: "HTTP",
		Status: domain.JobRunning, RetryCount: 0, MaxAttempts: 3, CreatedAt: time.Now().UTC(),
	}
	if err := repository.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	queue := &checkingQueue{repository: repository}
	service := NewService(
		repository, nil, nil, nil, nil, events.NewBroker(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	service.SetQueue(queue)
	service.SetRetryBase(time.Millisecond)

	if err := service.failJob(t.Context(), job, "CHECK_FAILED", errors.New("temporary")); err != nil {
		t.Fatal(err)
	}
	if !queue.called {
		t.Fatal("retry was not enqueued")
	}
}
