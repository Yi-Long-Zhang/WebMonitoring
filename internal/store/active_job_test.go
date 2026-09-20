package store

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
)

// activeJobRepositories returns every repository the active-job invariant has to
// hold for. The in-memory store mirrors the SQL partial unique index from
// migration 000012, and the two must agree: otherwise the non-database test
// suite accepts states PostgreSQL rejects, and the scheduler's error handling is
// only exercised on one of them.
func activeJobRepositories(t *testing.T) map[string]Repository {
	t.Helper()
	repositories := map[string]Repository{"memory": NewMemoryRepository()}
	if dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN")); dsn != "" {
		repositories["postgres"] = newMigratedPostgresRepository(t, dsn)
	}
	return repositories
}

// seedJobFixture creates the tenant, owner, asset and policy rows that a
// detection job references, and returns the tenant and asset ids.
func seedJobFixture(t *testing.T, repository Repository) (string, string) {
	t.Helper()
	suffix := identity.New()
	now := time.Now().UTC()
	tenant := domain.Tenant{ID: identity.New(), Slug: "active-job-" + suffix, Name: "active job", CreatedAt: now}
	if err := repository.CreateTenant(tenant); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	owner := domain.User{
		ID: identity.New(), TenantID: tenant.ID, Username: "owner-" + suffix,
		PasswordHash: "test-hash", Role: domain.RoleAdmin, CreatedAt: now,
	}
	if err := repository.CreateUser(owner); err != nil {
		t.Fatalf("create user: %v", err)
	}
	asset := domain.Asset{
		ID: identity.New(), TenantID: tenant.ID, Name: "asset-" + suffix,
		URL: "https://active-job-" + suffix + ".example", Type: domain.AssetOfficial,
		OwnerID: owner.ID, Enabled: true, VerificationStatus: domain.VerificationVerified,
		VerificationMethod: domain.VerificationFile, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateAsset(asset); err != nil {
		t.Fatalf("create asset: %v", err)
	}
	if err := repository.UpsertPolicy(domain.MonitorPolicy{
		ID: identity.New(), TenantID: tenant.ID, AssetID: asset.ID, Version: 1,
		HTTPIntervalSeconds: 300, BrowserIntervalSeconds: 1800, TimeoutSeconds: 30,
		MaxResponseBytes: 1024, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save policy: %v", err)
	}
	return tenant.ID, asset.ID
}

func newJob(tenantID, assetID, mode string, status domain.JobStatus) domain.DetectionJob {
	return domain.DetectionJob{
		ID: identity.New(), TenantID: tenantID, AssetID: assetID, Mode: mode,
		Status: status, PolicyVersion: 1, MaxAttempts: 3, CreatedAt: time.Now().UTC(),
	}
}

// TestCreateJobRejectsSecondActiveJob is the store-level half of the
// multi-replica fix. The scheduler decides a check is due by reading the job
// table and only then inserts, so two control-plane replicas can both insert an
// active job for one asset. The queue envelope carries no idempotency key, so
// without this constraint the duplicate is undetectable downstream.
func TestCreateJobRejectsSecondActiveJob(t *testing.T) {
	for name, repository := range activeJobRepositories(t) {
		t.Run(name, func(t *testing.T) {
			tenantID, assetID := seedJobFixture(t, repository)

			first := newJob(tenantID, assetID, "HTTP", domain.JobQueued)
			if err := repository.CreateJob(first); err != nil {
				t.Fatalf("first active job: %v", err)
			}
			for _, status := range []domain.JobStatus{domain.JobQueued, domain.JobRunning} {
				if err := repository.CreateJob(newJob(tenantID, assetID, "HTTP", status)); !errors.Is(err, ErrJobAlreadyActive) {
					t.Fatalf("second %s job err=%v, want ErrJobAlreadyActive", status, err)
				}
			}

			// A different mode is a different slot: the HTTP and BROWSER
			// schedules are tracked independently by due().
			if err := repository.CreateJob(newJob(tenantID, assetID, "BROWSER", domain.JobQueued)); err != nil {
				t.Fatalf("other mode should not conflict: %v", err)
			}

			// Releasing the slot must allow the next check. This is the path the
			// scheduler takes once a job reaches a terminal state.
			changed, err := repository.CancelJob(tenantID, first.ID)
			if err != nil || !changed {
				t.Fatalf("cancel first job: changed=%v err=%v", changed, err)
			}
			if err := repository.CreateJob(newJob(tenantID, assetID, "HTTP", domain.JobQueued)); err != nil {
				t.Fatalf("job after cancellation should be accepted: %v", err)
			}
		})
	}
}

// TestCreateJobIsolatesActiveJobsPerTenant guards the tenant boundary of the
// constraint: the same asset id under two tenants is two distinct slots.
func TestCreateJobIsolatesActiveJobsPerTenant(t *testing.T) {
	for name, repository := range activeJobRepositories(t) {
		t.Run(name, func(t *testing.T) {
			tenantID, assetID := seedJobFixture(t, repository)
			if err := repository.CreateJob(newJob(tenantID, assetID, "HTTP", domain.JobQueued)); err != nil {
				t.Fatalf("create job: %v", err)
			}
			other, otherAsset := seedJobFixture(t, repository)
			if other == tenantID {
				t.Fatal("fixture reused a tenant id")
			}
			if err := repository.CreateJob(newJob(other, otherAsset, "HTTP", domain.JobQueued)); err != nil {
				t.Fatalf("same mode on another tenant should not conflict: %v", err)
			}
		})
	}
}

// TestConcurrentCreateJobKeepsOneActiveJob races the insert the way two
// control-plane replicas would. Exactly one writer may win.
func TestConcurrentCreateJobKeepsOneActiveJob(t *testing.T) {
	for name, repository := range activeJobRepositories(t) {
		t.Run(name, func(t *testing.T) {
			tenantID, assetID := seedJobFixture(t, repository)
			const writers = 8
			results := make([]error, writers)
			var started sync.WaitGroup
			var finished sync.WaitGroup
			started.Add(writers)
			finished.Add(writers)
			for index := range writers {
				go func() {
					defer finished.Done()
					started.Done()
					started.Wait()
					results[index] = repository.CreateJob(newJob(tenantID, assetID, "HTTP", domain.JobQueued))
				}()
			}
			finished.Wait()

			accepted, rejected := 0, 0
			for _, err := range results {
				switch {
				case err == nil:
					accepted++
				case errors.Is(err, ErrJobAlreadyActive):
					rejected++
				default:
					t.Fatalf("unexpected error: %v", err)
				}
			}
			if accepted != 1 {
				t.Fatalf("accepted=%d rejected=%d, want exactly one accepted", accepted, rejected)
			}
			jobs, _, err := repository.ListJobs(tenantID, ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			active := 0
			for _, job := range jobs {
				if job.AssetID == assetID && job.Mode == "HTTP" && job.Status.IsActive() {
					active++
				}
			}
			if active != 1 {
				t.Fatalf("active jobs for the asset = %d, want 1", active)
			}
		})
	}
}
