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
	if err := runner.Up(ctx, true); err != nil {
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

	repository, err := NewPostgresRepository(ctx, dsn, testAlertSecretKey, DefaultPoolConfig())
	if err != nil {
		t.Fatalf("PostgreSQL repository unavailable: %v", err)
	}
	tenantID := "00000000-0000-4000-8000-000000000001"
	user := domain.User{
		ID: identity.New(), TenantID: tenantID, Username: "integration-" + identity.New(),
		PasswordHash: "test-hash", Role: domain.RoleAdmin, CreatedAt: time.Now().UTC(),
	}
	if err := repository.CreateUser(context.Background(), user); err != nil {
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
	if err := repository.CreateAsset(context.Background(), asset); err != nil {
		repository.Close()
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := repository.UpsertPolicy(context.Background(), domain.MonitorPolicy{
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
	if err := repository.CreateJob(context.Background(), job); err != nil {
		repository.Close()
		t.Fatal(err)
	}
	claimed, err := repository.ClaimJob(context.Background(), tenantID, job.ID, now)
	if err != nil || !claimed {
		repository.Close()
		t.Fatalf("claim queued job: claimed=%v err=%v", claimed, err)
	}
	claimedAgain, err := repository.ClaimJob(context.Background(), tenantID, job.ID, now.Add(time.Second))
	if err != nil || claimedAgain {
		repository.Close()
		t.Fatalf("reclaimed active job: claimed=%v err=%v", claimedAgain, err)
	}
	current, err := repository.GetJob(context.Background(), tenantID, job.ID)
	if err != nil {
		repository.Close()
		t.Fatal(err)
	}
	if current.Status != domain.JobRunning {
		repository.Close()
		t.Fatalf("job status=%s, want RUNNING", current.Status)
	}
	jobs, _, err := repository.ListJobs(context.Background(), tenantID, ListOptions{})
	if err != nil || len(jobs) == 0 {
		repository.Close()
		t.Fatalf("list jobs: count=%d err=%v", len(jobs), err)
	}

	repository.Close()
	reopened, err := NewPostgresRepository(ctx, dsn, testAlertSecretKey, DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.GetJob(context.Background(), tenantID, job.ID)
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
	if err := runner.Up(ctx, true); err != nil {
		t.Fatal(err)
	}

	repository, err := NewPostgresRepository(ctx, dsn, testAlertSecretKey, DefaultPoolConfig())
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
	if err := repository.CreateUser(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	// Names are unique per tenant, so the suite has to mint a fresh one to stay
	// re-runnable against a database that outlives a single test run.
	site := domain.Site{
		ID: identity.New(), TenantID: defaultTenant, Name: "scoped site " + identity.New(),
		Domain: "example-" + identity.New() + ".com", OwnerID: owner.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateSite(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	listed, _, err := repository.ListSites(context.Background(), defaultTenant, ListOptions{})
	if err != nil || len(listed) == 0 {
		t.Fatalf("list sites: count=%d err=%v", len(listed), err)
	}
	if _, err := repository.GetSite(context.Background(), defaultTenant, site.ID); err != nil {
		t.Fatalf("get site: %v", err)
	}

	otherTenant := domain.Tenant{ID: identity.New(), Slug: "integration-" + identity.New(), Name: "Other", CreatedAt: now}
	if err := repository.CreateTenant(context.Background(), otherTenant); err != nil {
		t.Fatal(err)
	}
	foreignOwner := domain.User{
		ID: identity.New(), TenantID: otherTenant.ID, Username: "foreign-owner-" + identity.New(),
		PasswordHash: "test-hash", Role: domain.RoleAdmin, CreatedAt: now,
	}
	if err := repository.CreateUser(context.Background(), foreignOwner); err != nil {
		t.Fatal(err)
	}

	// A site whose owner belongs to a different tenant must be rejected by the
	// composite (tenant_id, owner_id) foreign key.
	crossOwner := domain.Site{
		ID: identity.New(), TenantID: defaultTenant, Name: "cross-owner site " + identity.New(),
		Domain: "example-" + identity.New() + ".com", OwnerID: foreignOwner.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateSite(context.Background(), crossOwner); err == nil {
		t.Fatal("cross-tenant site owner was accepted")
	}

	foreignSite := domain.Site{
		ID: identity.New(), TenantID: otherTenant.ID, Name: "foreign site " + identity.New(),
		Domain: "example-" + identity.New() + ".com", OwnerID: foreignOwner.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateSite(context.Background(), foreignSite); err != nil {
		t.Fatal(err)
	}

	// An asset in the default tenant referencing a site from another tenant must
	// be rejected by the composite (tenant_id, site_id) foreign key.
	crossAsset := domain.Asset{
		ID: identity.New(), TenantID: defaultTenant, SiteID: foreignSite.ID, Name: "cross-site asset",
		URL: "https://example-" + identity.New() + ".com", Type: domain.AssetOfficial, OwnerID: owner.ID,
		Enabled: true, VerificationStatus: domain.VerificationVerified, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateAsset(context.Background(), crossAsset); err == nil {
		t.Fatal("cross-tenant site reference on asset was accepted")
	}
}

// TestPostgresKeysetPaginationWalksEveryRowOnce pins the SQL that paginateQuery
// builds. SQL requires ORDER BY to precede LIMIT, so the ordering and the LIMIT
// have to be emitted together; a list query that appends them in the wrong order
// fails at parse time and only ever shows up against a real server. The test also
// asserts that the cursor resumes exactly after the previous page and that a
// second tenant's rows never leak into the walk.
func TestPostgresKeysetPaginationWalksEveryRowOnce(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	runner, err := migrate.New(ctx, dsn)
	if err != nil {
		t.Fatalf("PostgreSQL integration test unavailable: %v", err)
	}
	defer runner.Close()
	if err := runner.Up(ctx, true); err != nil {
		t.Fatal(err)
	}

	repository, err := NewPostgresRepository(ctx, dsn, testAlertSecretKey, DefaultPoolConfig())
	if err != nil {
		t.Fatalf("PostgreSQL repository unavailable: %v", err)
	}
	defer repository.Close()

	now := time.Now().UTC()
	tenant := domain.Tenant{ID: identity.New(), Slug: "pagination-" + identity.New(), Name: "Pagination", CreatedAt: now}
	if err := repository.CreateTenant(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	owner := domain.User{
		ID: identity.New(), TenantID: tenant.ID, Username: "pagination-owner-" + identity.New(),
		PasswordHash: "test-hash", Role: domain.RoleAdmin, CreatedAt: now,
	}
	if err := repository.CreateUser(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	foreign := domain.Tenant{ID: identity.New(), Slug: "pagination-foreign-" + identity.New(), Name: "Foreign", CreatedAt: now}
	if err := repository.CreateTenant(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	foreignOwner := domain.User{
		ID: identity.New(), TenantID: foreign.ID, Username: "pagination-foreign-owner-" + identity.New(),
		PasswordHash: "test-hash", Role: domain.RoleAdmin, CreatedAt: now,
	}
	if err := repository.CreateUser(context.Background(), foreignOwner); err != nil {
		t.Fatal(err)
	}

	// The walk is descending on created_at, so the newest row has to come first.
	const total = 5
	base := now.Add(-time.Hour)
	expected := make([]string, 0, total)
	for index := range total {
		asset := domain.Asset{
			ID: identity.New(), TenantID: tenant.ID, Name: "pagination asset",
			URL:  "https://pagination-" + identity.New() + ".example",
			Type: domain.AssetOfficial, OwnerID: owner.ID, Enabled: true,
			VerificationStatus: domain.VerificationVerified,
			CreatedAt:          base.Add(time.Duration(index) * time.Second), UpdatedAt: now,
		}
		if err := repository.CreateAsset(context.Background(), asset); err != nil {
			t.Fatal(err)
		}
		expected = append([]string{asset.ID}, expected...)
	}
	foreignAsset := domain.Asset{
		ID: identity.New(), TenantID: foreign.ID, Name: "foreign pagination asset",
		URL:  "https://pagination-foreign-" + identity.New() + ".example",
		Type: domain.AssetOfficial, OwnerID: foreignOwner.ID, Enabled: true,
		VerificationStatus: domain.VerificationVerified,
		CreatedAt:          base.Add(time.Hour), UpdatedAt: now,
	}
	if err := repository.CreateAsset(context.Background(), foreignAsset); err != nil {
		t.Fatal(err)
	}

	const pageSize = 2
	pageSizes := make([]int, 0, 3)
	seen := make([]string, 0, total)
	cursor := ""
	for page := 1; ; page++ {
		if page > total+1 {
			t.Fatal("keyset pagination did not terminate")
		}
		items, next, err := repository.ListAssets(context.Background(), tenant.ID, ListOptions{Limit: pageSize, Cursor: cursor})
		if err != nil {
			t.Fatalf("assets page %d: %v", page, err)
		}
		if len(items) > pageSize {
			t.Fatalf("assets page %d returned %d rows, want at most %d", page, len(items), pageSize)
		}
		pageSizes = append(pageSizes, len(items))
		for _, item := range items {
			if item.TenantID != tenant.ID {
				t.Fatalf("assets page %d leaked tenant %s", page, item.TenantID)
			}
			seen = append(seen, item.ID)
		}
		if next == "" {
			break
		}
		cursor = next
	}

	if len(pageSizes) != 3 || pageSizes[0] != 2 || pageSizes[1] != 2 || pageSizes[2] != 1 {
		t.Fatalf("page sizes=%v, want [2 2 1]", pageSizes)
	}
	if len(seen) != total {
		t.Fatalf("walked %d assets, want %d", len(seen), total)
	}
	for index := range expected {
		if seen[index] != expected[index] {
			t.Fatalf("walk position %d = %s, want %s (descending created_at order)", index, seen[index], expected[index])
		}
	}

	// ListTenants shares the helper but has no tenant predicate at all.
	tenants, _, err := repository.ListTenants(context.Background(), ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("list tenants: %v", err)
	}
	if len(tenants) < 1 || len(tenants) > 2 {
		t.Fatalf("limited tenant list returned %d rows, want 1..2", len(tenants))
	}
}

const testAlertSecretKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// TestAlertChannelUpdateKeepsCiphertextReadable covers the reason the update is
// an UPDATE and not a delete-then-insert. The encrypted columns are bound to the
// row's id and tenant through their associated data, so any implementation that
// recreated the row would leave a channel whose stored credential can never be
// decrypted again -- and the dispatcher would sign every webhook with garbage
// while nothing reported an error.
func TestAlertChannelUpdateKeepsCiphertextReadable(t *testing.T) {
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
	if err := runner.Up(ctx, true); err != nil {
		t.Fatal(err)
	}

	repository, err := NewPostgresRepository(ctx, dsn, testAlertSecretKey, DefaultPoolConfig())
	if err != nil {
		t.Fatalf("PostgreSQL repository unavailable: %v", err)
	}
	defer repository.Close()

	tenantID := "00000000-0000-4000-8000-000000000001"
	now := time.Now().UTC()
	channel := domain.AlertChannel{
		ID: identity.New(), TenantID: tenantID, Name: "integration channel " + identity.New(),
		Type: "WEBHOOK", URL: "https://hooks.example.com/integration",
		Secret: "integration-secret", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateAlertChannel(context.Background(), channel); err != nil {
		t.Fatal(err)
	}
	before, err := repository.ListAlertChannelsWithSecrets(context.Background(), tenantID)
	if err != nil {
		t.Fatal(err)
	}
	var stored domain.AlertChannel
	for _, item := range before {
		if item.ID == channel.ID {
			stored = item
		}
	}
	if stored.ID != channel.ID {
		t.Fatal("the created channel was not returned by ListAlertChannelsWithSecrets")
	}
	if stored.Secret != "integration-secret" {
		t.Fatalf("stored secret round-tripped as %q", stored.Secret)
	}

	// A rename that keeps the credential. The stored value is what the API layer
	// carries forward on a blank submit, so the test does the same.
	renamed := stored
	renamed.Name = "integration channel renamed"
	renamed.UpdatedAt = time.Now().UTC()
	if err := repository.UpdateAlertChannel(context.Background(), renamed); err != nil {
		t.Fatal(err)
	}

	after, err := repository.ListAlertChannelsWithSecrets(context.Background(), tenantID)
	if err != nil {
		t.Fatal(err)
	}
	var updated domain.AlertChannel
	for _, item := range after {
		if item.ID == channel.ID {
			updated = item
		}
	}
	if updated.ID != channel.ID {
		t.Fatalf("the update changed the row identity: %q", updated.ID)
	}
	if updated.Name != "integration channel renamed" {
		t.Fatalf("update did not apply: name=%q", updated.Name)
	}
	// The credential has to survive an in-place rewrite, not just a read.
	if updated.Secret != "integration-secret" {
		t.Fatalf("the stored credential no longer decrypts after an update: %q", updated.Secret)
	}

	// A tenant that does not own the row cannot rewrite it, and the miss has to
	// be reported rather than silently doing nothing.
	other := "00000000-0000-4000-8000-000000000002"
	crossTenant := renamed
	crossTenant.TenantID = other
	if err := repository.UpdateAlertChannel(context.Background(), crossTenant); err != ErrNotFound {
		t.Fatalf("cross-tenant update returned %v, want ErrNotFound", err)
	}
	if err := repository.DeleteAlertChannel(context.Background(), other, channel.ID); err != ErrNotFound {
		t.Fatalf("cross-tenant delete returned %v, want ErrNotFound", err)
	}

	if err := repository.DeleteAlertChannel(context.Background(), tenantID, channel.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetAlertChannel(context.Background(), tenantID, channel.ID); err != ErrNotFound {
		t.Fatalf("the channel survived the delete: %v", err)
	}
}
