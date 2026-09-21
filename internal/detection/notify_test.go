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

// recordingAlertQueue captures what would be queued, and can be made to fail.
type recordingAlertQueue struct {
	jobs []domain.AlertJob
	err  error
}

func (q *recordingAlertQueue) EnqueueAlert(_ context.Context, job domain.AlertJob) error {
	if q.err != nil {
		return q.err
	}
	q.jobs = append(q.jobs, job)
	return nil
}

func newNotifyService(repository store.Repository) *Service {
	return NewService(repository, nil, nil, nil, nil, events.NewBroker(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestNotifyIncidentCarriesTheReasonOntoTheQueue is the whole point of the
// change: the queued job has to name what happened, because the delivery key is
// built from it and the message headline is rendered from it. A job without one
// would collapse every event about an incident into the first.
func TestNotifyIncidentCarriesTheReasonOntoTheQueue(t *testing.T) {
	repository := store.NewMemoryRepository()
	service := newNotifyService(repository)
	queue := &recordingAlertQueue{}
	service.SetAlertQueue(queue)
	incident := domain.Incident{ID: "incident", TenantID: "tenant", AssetID: "asset"}

	service.NotifyIncident(t.Context(), incident, domain.AlertReasonTransitioned)
	service.NotifyIncident(t.Context(), incident, domain.AlertReasonCommented)

	if len(queue.jobs) != 2 {
		t.Fatalf("expected two queued alerts, got %d", len(queue.jobs))
	}
	if queue.jobs[0].Reason != domain.AlertReasonTransitioned || queue.jobs[1].Reason != domain.AlertReasonCommented {
		t.Fatalf("reasons were not carried through: %#v", queue.jobs)
	}
	for _, job := range queue.jobs {
		if job.IncidentID != "incident" || job.TenantID != "tenant" || job.AssetID != "asset" {
			t.Fatalf("queued job lost its identifiers: %#v", job)
		}
	}
}

// TestNotifyIncidentIsSilentWithoutANotifier: tests, the embedded mode and any
// deployment that has configured no alert path must not panic or error when an
// incident is transitioned. A status change is not conditional on being
// announceable.
func TestNotifyIncidentIsSilentWithoutANotifier(t *testing.T) {
	service := newNotifyService(store.NewMemoryRepository())
	// No notifier and no queue: this must simply do nothing.
	service.NotifyIncident(t.Context(), domain.Incident{ID: "incident", TenantID: "tenant"}, domain.AlertReasonTransitioned)
}

// TestNotifyIncidentSurvivesAFailingQueue. The incident has already changed by
// the time a notification is raised, so a queue outage must not propagate back
// as a failed transition -- that would tell the caller a status change did not
// happen when it did.
func TestNotifyIncidentSurvivesAFailingQueue(t *testing.T) {
	service := newNotifyService(store.NewMemoryRepository())
	service.SetAlertQueue(&recordingAlertQueue{err: errors.New("redis down")})
	service.NotifyIncident(t.Context(), domain.Incident{ID: "incident", TenantID: "tenant"}, domain.AlertReasonTransitioned)
}

// TestNotifyIncidentDoesNotQueueWithoutAnAsset is a guard on a mistake rather
// than a feature: an incident with no asset cannot be rendered into a message,
// and queueing it would park a job that fails forever in the alert worker.
func TestNotifyIncidentFallsBackToInlineDeliveryWithoutAQueue(t *testing.T) {
	repository := store.NewMemoryRepository()
	if err := repository.CreateAsset(domain.Asset{
		ID: "asset", TenantID: "tenant", Name: "site", URL: "https://example.com",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	// Notifier present (constructed in NewService) but no queue: the inline path
	// runs. With no channels configured it has nothing to send to and must not
	// report a failure.
	service := newNotifyService(repository)
	service.NotifyIncident(t.Context(), domain.Incident{
		ID: "incident", TenantID: "tenant", AssetID: "asset",
	}, domain.AlertReasonTransitioned)
}
