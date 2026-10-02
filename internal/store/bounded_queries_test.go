package store

import (
	"context"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
)

// seedBoundedFixture builds one tenant with three assets (two verified, one
// pending), an approved baseline on the first asset, and job/incident rows in
// the states the bounded-query tests assert on. Jobs are created directly in
// their final status: these tests measure reads and deletes, not the job
// lifecycle.
func seedBoundedFixture(t *testing.T, repository Repository) boundedFixture {
	t.Helper()
	// Truncated to microseconds because PostgreSQL stores timestamptz at
	// microsecond resolution: a nanosecond-precision expectation would never
	// compare equal to the value that comes back, and the tests below assert
	// exact times rather than orderings.
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := identity.New()
	tenant := domain.Tenant{ID: identity.New(), Slug: "bounded-" + suffix, Name: "bounded", CreatedAt: now}
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
	newAsset := func(name string, verified bool) domain.Asset {
		status := domain.VerificationPending
		if verified {
			status = domain.VerificationVerified
		}
		return domain.Asset{
			ID: identity.New(), TenantID: tenant.ID, Name: name,
			URL: "https://" + name + "-" + suffix + ".example", Type: domain.AssetOfficial,
			OwnerID: owner.ID, Enabled: verified, VerificationStatus: status,
			VerificationMethod: domain.VerificationFile, CreatedAt: now, UpdatedAt: now,
		}
	}
	verifiedOne, verifiedTwo, pending := newAsset("one", true), newAsset("two", true), newAsset("three", false)
	for _, asset := range []domain.Asset{verifiedOne, verifiedTwo, pending} {
		if err := repository.CreateAsset(context.Background(), asset); err != nil {
			t.Fatalf("create asset: %v", err)
		}
	}
	baseline := domain.BaselineVersion{
		ID: identity.New(), TenantID: tenant.ID, AssetID: verifiedOne.ID, Version: 1,
		Status: domain.BaselineApproved, CreatedBy: owner.ID, CreatedAt: now,
	}
	if err := repository.CreateBaseline(context.Background(), baseline); err != nil {
		t.Fatalf("create baseline: %v", err)
	}

	old := now.Add(-40 * 24 * time.Hour)
	recent := now.Add(-24 * time.Hour)
	createJob := func(assetID, mode string, status domain.JobStatus, createdAt time.Time) domain.DetectionJob {
		job := domain.DetectionJob{
			ID: identity.New(), TenantID: tenant.ID, AssetID: assetID, Mode: mode,
			Status: status, PolicyVersion: 1, MaxAttempts: 3, CreatedAt: createdAt,
		}
		if status == domain.JobRunning {
			job.StartedAt = &createdAt
		}
		if !status.IsActive() && status != domain.JobQueued {
			job.FinishedAt = &createdAt
		}
		if err := repository.CreateJob(context.Background(), job); err != nil {
			t.Fatalf("create job: %v", err)
		}
		return job
	}
	// verifiedOne/HTTP: an old terminal row (retention-eligible) and a recent
	// one (kept). verifiedOne/BROWSER: never checked. verifiedTwo/HTTP: an old
	// RUNNING row -- active state must survive retention.
	oldSucceeded := createJob(verifiedOne.ID, "HTTP", domain.JobSucceeded, old)
	recentSucceeded := createJob(verifiedOne.ID, "HTTP", domain.JobSucceeded, recent)
	createJob(verifiedOne.ID, "HTTP", domain.JobFailed, old)
	oldRunning := createJob(verifiedTwo.ID, "HTTP", domain.JobRunning, old)

	// Fingerprints land in a char(64) column, so each one must be exactly 64
	// printable characters -- and differ per (severity, status) so every call
	// inserts its own row rather than colliding on the active-fingerprint index.
	newIncident := func(severity domain.Severity, status domain.IncidentStatus) {
		tag := string(severity) + "-" + string(status)
		base := make([]byte, 64-len(tag))
		for index := range base {
			base[index] = 'a' + byte(index%26)
		}
		incident := domain.Incident{
			ID: identity.New(), TenantID: tenant.ID, AssetID: verifiedOne.ID,
			BaselineID: baseline.ID, BaselineVersion: 1, Fingerprint: string(base) + tag,
			Severity: severity, Status: status, Title: "changed", CreatedAt: now,
			FirstSeenAt: now, LastSeenAt: now,
		}
		if _, _, err := repository.UpsertIncident(context.Background(), incident); err != nil {
			t.Fatalf("create incident: %v", err)
		}
	}
	// Two open (one CRITICAL), one closed HIGH, one dismissed CRITICAL. Only
	// the open pair counts, and only the open CRITICAL is high-risk+critical.
	newIncident(domain.SeverityCritical, domain.IncidentOpen)
	newIncident(domain.SeverityLow, domain.IncidentOpen)
	newIncident(domain.SeverityHigh, domain.IncidentClosed)
	newIncident(domain.SeverityCritical, domain.IncidentFalsePositive)

	return boundedFixture{
		tenantID: tenant.ID, verifiedOne: verifiedOne.ID, verifiedTwo: verifiedTwo.ID,
		oldSucceeded: oldSucceeded, recentSucceeded: recentSucceeded, oldRunning: oldRunning,
	}
}

type boundedFixture struct {
	tenantID        string
	verifiedOne     string
	verifiedTwo     string
	oldSucceeded    domain.DetectionJob
	recentSucceeded domain.DetectionJob
	oldRunning      domain.DetectionJob
}

func activityByPair(t *testing.T, repository Repository, tenantID string) map[string]JobActivity {
	t.Helper()
	activity, err := repository.JobSchedulingState(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("job scheduling state: %v", err)
	}
	result := make(map[string]JobActivity, len(activity))
	for _, item := range activity {
		result[item.AssetID+"/"+item.Mode] = item
	}
	return result
}

func TestJobSchedulingStateSummarizesHistory(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			fixture := seedBoundedFixture(t, repository)
			state := activityByPair(t, repository, fixture.tenantID)

			http, ok := state[fixture.verifiedOne+"/HTTP"]
			if !ok {
				t.Fatal("verifiedOne/HTTP missing from scheduling state")
			}
			if !http.LatestCreatedAt.Equal(fixture.recentSucceeded.CreatedAt) {
				t.Fatalf("latest = %v, want the recent job at %v", http.LatestCreatedAt, fixture.recentSucceeded.CreatedAt)
			}
			if http.HasActive {
				t.Fatal("terminal jobs must not mark the pair active")
			}
			running, ok := state[fixture.verifiedTwo+"/HTTP"]
			if !ok {
				t.Fatal("verifiedTwo/HTTP missing from scheduling state")
			}
			if !running.HasActive {
				t.Fatal("RUNNING job must mark the pair active")
			}
			if !running.LatestCreatedAt.Equal(fixture.oldRunning.CreatedAt) {
				t.Fatalf("running pair latest = %v, want %v", running.LatestCreatedAt, fixture.oldRunning.CreatedAt)
			}
			if _, ok := state[fixture.verifiedOne+"/BROWSER"]; ok {
				t.Fatal("a pair with no jobs must not appear in the state")
			}
		})
	}
}

func TestDeleteJobsBeforeKeepsActiveRows(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			fixture := seedBoundedFixture(t, repository)
			cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)

			deleted, err := repository.DeleteJobsBefore(context.Background(), fixture.tenantID, cutoff)
			if err != nil {
				t.Fatalf("delete jobs: %v", err)
			}
			// The old SUCCEEDED and old FAILED rows go; the recent row and the
			// old RUNNING row (crash-recovery state) stay.
			if deleted != 2 {
				t.Fatalf("deleted = %d, want 2", deleted)
			}
			if _, err := repository.GetJob(context.Background(), fixture.tenantID, fixture.oldSucceeded.ID); err != ErrNotFound {
				t.Fatalf("old terminal job survived: %v", err)
			}
			if _, err := repository.GetJob(context.Background(), fixture.tenantID, fixture.oldRunning.ID); err != nil {
				t.Fatalf("active job was deleted: %v", err)
			}
			if _, err := repository.GetJob(context.Background(), fixture.tenantID, fixture.recentSucceeded.ID); err != nil {
				t.Fatalf("recent terminal job was deleted: %v", err)
			}
		})
	}
}

func TestCountAssetsAndIncidentAndJobCountsAgreeAcrossStores(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			fixture := seedBoundedFixture(t, repository)

			total, verified, err := repository.CountAssets(context.Background(), fixture.tenantID)
			if err != nil {
				t.Fatalf("count assets: %v", err)
			}
			if total != 3 || verified != 2 {
				t.Fatalf("assets = %d/%d, want 3/2", total, verified)
			}

			open, highRisk, critical, err := repository.IncidentStatusCounts(context.Background(), fixture.tenantID)
			if err != nil {
				t.Fatalf("incident counts: %v", err)
			}
			if open != 2 || highRisk != 1 || critical != 1 {
				t.Fatalf("incidents = open %d high %d critical %d, want 2/1/1", open, highRisk, critical)
			}

			failed, err := repository.FailedJobCount(context.Background(), fixture.tenantID)
			if err != nil {
				t.Fatalf("failed job count: %v", err)
			}
			if failed != 1 {
				t.Fatalf("failed jobs = %d, want 1 (only the old FAILED row)", failed)
			}
		})
	}
}

func TestJobSchedulingStateIsTenantScoped(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			fixture := seedBoundedFixture(t, repository)
			other := seedBoundedFixture(t, repository)

			state := activityByPair(t, repository, fixture.tenantID)
			for _, item := range state {
				if item.AssetID == other.verifiedOne || item.AssetID == other.verifiedTwo {
					t.Fatalf("scheduling state leaked another tenant's asset %s", item.AssetID)
				}
			}
			deleted, err := repository.DeleteJobsBefore(context.Background(), fixture.tenantID, time.Now().UTC())
			if err != nil {
				t.Fatalf("delete jobs: %v", err)
			}
			if deleted == 0 {
				t.Fatal("expected fixture rows to be retention-eligible")
			}
			// The other tenant's rows must be untouched by the sweep.
			if otherState := activityByPair(t, repository, other.tenantID); len(otherState) == 0 {
				t.Fatal("tenant sweep deleted another tenant's jobs")
			}
		})
	}
}
