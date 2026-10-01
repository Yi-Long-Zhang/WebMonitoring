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
	"webmonitoring/internal/identity"
	"webmonitoring/internal/store"
)

// recordingQueue captures what the scheduler would hand to the workers, so the
// concurrency test can assert on queueing without a Redis stream.
type recordingQueue struct {
	mu   sync.Mutex
	jobs []domain.DetectionJob
}

func (q *recordingQueue) Enqueue(_ context.Context, job domain.DetectionJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, job)
	return nil
}

func (q *recordingQueue) enqueued() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.jobs)
}

// newSchedulerFixture builds one enabled, verified asset on a 300 second HTTP
// interval. The browser interval is zero so due() only ever considers HTTP.
func newSchedulerFixture(t *testing.T) (*Scheduler, store.Repository, *recordingQueue, string, string) {
	t.Helper()
	repository := store.NewMemoryRepository()
	now := time.Now().UTC()
	suffix := identity.New()
	tenant := domain.Tenant{ID: identity.New(), Slug: "sched-" + suffix, Name: "sched", CreatedAt: now}
	if err := repository.CreateTenant(context.Background(), tenant); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	owner := domain.User{
		ID: identity.New(), TenantID: tenant.ID, Username: "owner-" + suffix,
		PasswordHash: "test-hash", Role: domain.RoleAdmin, CreatedAt: now,
	}
	if err := repository.CreateUser(context.Background(), owner); err != nil {
		t.Fatalf("create user: %v", err)
	}
	asset := domain.Asset{
		ID: identity.New(), TenantID: tenant.ID, Name: "asset-" + suffix,
		URL: "https://sched-" + suffix + ".example", Type: domain.AssetOfficial,
		OwnerID: owner.ID, Enabled: true, VerificationStatus: domain.VerificationVerified,
		VerificationMethod: domain.VerificationFile, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateAsset(context.Background(), asset); err != nil {
		t.Fatalf("create asset: %v", err)
	}
	if err := repository.UpsertPolicy(context.Background(), domain.MonitorPolicy{
		ID: identity.New(), TenantID: tenant.ID, AssetID: asset.ID, Version: 1,
		HTTPIntervalSeconds: 300, BrowserIntervalSeconds: 0, TimeoutSeconds: 30,
		MaxResponseBytes: 1024, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save policy: %v", err)
	}
	queue := &recordingQueue{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := NewService(repository, nil, nil, nil, nil, events.NewBroker(), logger)
	service.SetQueue(queue)
	return NewScheduler(repository, service, logger), repository, queue, tenant.ID, asset.ID
}

// TestConcurrentReplicasQueueOneJobPerAsset pins the interleaving that due()
// cannot rule out: every control-plane replica reads the job table and sees no
// in-flight check for the asset before any of them inserts. The empty job slice
// is passed in explicitly rather than re-read, because racing real schedule()
// calls through the in-memory store's mutex serialises them and hides the race.
//
// Exactly one replica may win. The losers are rejected by the active-job index
// and swallowed by queueIfDue, so a duplicate never reaches the workers.
func TestConcurrentReplicasQueueOneJobPerAsset(t *testing.T) {
	const replicas = 6
	scheduler, repository, queue, tenantID, assetID := newSchedulerFixture(t)
	now := time.Now().UTC()

	var started sync.WaitGroup
	var finished sync.WaitGroup
	started.Add(replicas)
	finished.Add(replicas)
	for range replicas {
		go func() {
			defer finished.Done()
			started.Done()
			started.Wait()
			scheduler.queueIfDue(context.Background(), tenantID, assetID, "HTTP", 300, store.JobActivity{}, now)
		}()
	}
	finished.Wait()

	jobs, _, err := repository.ListJobs(context.Background(), tenantID, store.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, job := range jobs {
		if job.Mode == "HTTP" && job.Status.IsActive() {
			active++
		}
	}
	if len(jobs) != 1 || active != 1 {
		t.Fatalf("jobs=%d active=%d, want exactly one queued HTTP job", len(jobs), active)
	}
	if enqueued := queue.enqueued(); enqueued != 1 {
		t.Fatalf("enqueued=%d, want 1", enqueued)
	}
}

// TestRepeatedScheduleDoesNotQueueWhileJobIsActive pins the single-replica path:
// a second tick before the interval elapses must not queue again.
func TestRepeatedScheduleDoesNotQueueWhileJobIsActive(t *testing.T) {
	scheduler, repository, queue, tenantID, _ := newSchedulerFixture(t)
	scheduler.schedule(context.Background(), tenantID)
	scheduler.schedule(context.Background(), tenantID)
	scheduler.schedule(context.Background(), tenantID)

	jobs, _, err := repository.ListJobs(context.Background(), tenantID, store.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs=%d, want 1", len(jobs))
	}
	if enqueued := queue.enqueued(); enqueued != 1 {
		t.Fatalf("enqueued=%d, want 1", enqueued)
	}
}

// due() is the scheduling decision in isolation: the aggregate row alone must
// decide, so a history of a million finished jobs cannot change the answer.
func TestDueTable(t *testing.T) {
	now := time.Now().UTC()
	recent := now.Add(-time.Minute)
	old := now.Add(-time.Hour)
	cases := []struct {
		label    string
		interval int
		item     store.JobActivity
		want     bool
	}{
		{"disabled interval never due", 0, store.JobActivity{}, false},
		{"never checked is due", 300, store.JobActivity{}, true},
		{"active job blocks regardless of age", 300, store.JobActivity{LatestCreatedAt: old, HasActive: true}, false},
		{"interval not elapsed", 300, store.JobActivity{LatestCreatedAt: recent}, false},
		{"interval elapsed", 60, store.JobActivity{LatestCreatedAt: old}, true},
	}
	for _, testCase := range cases {
		if got := due(testCase.interval, testCase.item, now); got != testCase.want {
			t.Fatalf("%s: due = %v, want %v", testCase.label, got, testCase.want)
		}
	}
}
