package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/migrate"
)

func TestPostgresMigrationJobClaimAndRestartRecovery(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	runner, err := migrate.New(ctx, dsn)
	if err != nil {
		t.Fatalf("PostgreSQL integration test unavailable: %v", err)
	}
	defer runner.Close()
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	statuses, err := runner.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status.AppliedAt == nil {
			t.Fatalf("migration %d is still pending", status.Version)
		}
	}

	repository, err := NewPostgresRepository(ctx, dsn, testAlertSecretKey)
	if err != nil {
		t.Fatalf("PostgreSQL repository unavailable: %v", err)
	}
	tenantID := "00000000-0000-4000-8000-000000000001"
	user := domain.User{
		ID: identity.New(), TenantID: tenantID, Username: "integration-" + identity.New(),
		PasswordHash: "test-hash", Role: domain.RoleAdmin, CreatedAt: time.Now().UTC(),
	}
	if err := repository.CreateUser(user); err != nil {
		repository.Close()
		t.Fatal(err)
	}
	asset := domain.Asset{
		ID: identity.New(), TenantID: tenantID, Name: "integration asset",
		URL:  "https://integration-" + identity.New() + ".example",
		Type: domain.AssetOfficial, OwnerID: user.ID, Enabled: true,
		VerificationStatus: domain.VerificationVerified,
		CreatedAt:          time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repository.CreateAsset(asset); err != nil {
		repository.Close()
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := repository.UpsertPolicy(domain.MonitorPolicy{
		ID: identity.New(), TenantID: tenantID, AssetID: asset.ID, Version: 1,
		HTTPIntervalSeconds: 300, BrowserIntervalSeconds: 1800, TimeoutSeconds: 30,
		MaxResponseBytes: 1024, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		repository.Close()
		t.Fatal(err)
	}
	job := domain.DetectionJob{
		ID: identity.New(), TenantID: tenantID, AssetID: asset.ID, Mode: "HTTP",
		Status: domain.JobQueued, PolicyVersion: 1, MaxAttempts: 3, CreatedAt: now,
	}
	if err := repository.CreateJob(job); err != nil {
		repository.Close()
		t.Fatal(err)
	}
	claimed, err := repository.ClaimJob(tenantID, job.ID, now)
	if err != nil || !claimed {
		repository.Close()
		t.Fatalf("claim queued job: claimed=%v err=%v", claimed, err)
	}
	claimedAgain, err := repository.ClaimJob(tenantID, job.ID, now.Add(time.Second))
	if err != nil || claimedAgain {
		repository.Close()
		t.Fatalf("reclaimed active job: claimed=%v err=%v", claimedAgain, err)
	}
	current, err := repository.GetJob(tenantID, job.ID)
	if err != nil {
		repository.Close()
		t.Fatal(err)
	}
	if current.Status != domain.JobRunning {
		repository.Close()
		t.Fatalf("job status=%s, want RUNNING", current.Status)
	}
	jobs, _, err := repository.ListJobs(tenantID, ListOptions{})
	if err != nil || len(jobs) == 0 {
		repository.Close()
		t.Fatalf("list jobs: count=%d err=%v", len(jobs), err)
	}

	repository.Close()
	reopened, err := NewPostgresRepository(ctx, dsn, testAlertSecretKey)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.GetJob(tenantID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != domain.JobRunning {
		t.Fatalf("restarted job status=%s, want RUNNING", persisted.Status)
	}
}

func TestPostgresSitesEnforceTenantScoping(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	runner, err := migrate.New(ctx, dsn)
	if err != nil {
		t.Fatalf("PostgreSQL integration test unavailable: %v", err)
	}
	defer runner.Close()
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}

	repository, err := NewPostgresRepository(ctx, dsn, testAlertSecretKey)
	if err != nil {
		t.Fatalf("PostgreSQL repository unavailable: %v", err)
	}
	defer repository.Close()

	const defaultTenant = "00000000-0000-4000-8000-000000000001"
	now := time.Now().UTC()

	owner := domain.User{
		ID: identity.New(), TenantID: defaultTenant, Username: "site-owner-" + identity.New(),
		PasswordHash: "test-hash", Role: domain.RoleAdmin, CreatedAt: now,
	}
	if err := repository.CreateUser(owner); err != nil {
		t.Fatal(err)
	}
	site := domain.Site{
		ID: identity.New(), TenantID: defaultTenant, Name: "scoped site",
		Domain: "example-" + identity.New() + ".com", OwnerID: owner.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateSite(site); err != nil {
		t.Fatal(err)
	}
	listed, _, err := repository.ListSites(defaultTenant, ListOptions{})
	if err != nil || len(listed) == 0 {
		t.Fatalf("list sites: count=%d err=%v", len(listed), err)
	}
	if _, err := repository.GetSite(defaultTenant, site.ID); err != nil {
		t.Fatalf("get site: %v", err)
	}

	otherTenant := domain.Tenant{ID: identity.New(), Slug: "integration-" + identity.New(), Name: "Other", CreatedAt: now}
	if err := repository.CreateTenant(otherTenant); err != nil {
		t.Fatal(err)
	}
	foreignOwner := domain.User{
		ID: identity.New(), TenantID: otherTenant.ID, Username: "foreign-owner-" + identity.New(),
		PasswordHash: "test-hash", Role: domain.RoleAdmin, CreatedAt: now,
	}
	if err := repository.CreateUser(foreignOwner); err != nil {
		t.Fatal(err)
	}

	// A site whose owner belongs to a different tenant must be rejected by the
	// composite (tenant_id, owner_id) foreign key.
	crossOwner := domain.Site{
		ID: identity.New(), TenantID: defaultTenant, Name: "cross-owner site",
		Domain: "example-" + identity.New() + ".com", OwnerID: foreignOwner.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateSite(crossOwner); err == nil {
		t.Fatal("cross-tenant site owner was accepted")
	}

	foreignSite := domain.Site{
		ID: identity.New(), TenantID: otherTenant.ID, Name: "foreign site",
		Domain: "example-" + identity.New() + ".com", OwnerID: foreignOwner.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateSite(foreignSite); err != nil {
		t.Fatal(err)
	}

	// An asset in the default tenant referencing a site from another tenant must
	// be rejected by the composite (tenant_id, site_id) foreign key.
	crossAsset := domain.Asset{
		ID: identity.New(), TenantID: defaultTenant, SiteID: foreignSite.ID, Name: "cross-site asset",
		URL: "https://example-" + identity.New() + ".com", Type: domain.AssetOfficial, OwnerID: owner.ID,
		Enabled: true, VerificationStatus: domain.VerificationVerified, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateAsset(crossAsset); err == nil {
		t.Fatal("cross-tenant site reference on asset was accepted")
	}
}

const testAlertSecretKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
