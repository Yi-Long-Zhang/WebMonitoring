package maintenance

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/store"
)

const testTenant = "00000000-0000-4000-8000-000000000001"

func newTestRunner(t *testing.T) (*store.MemoryRepository, evidence.Store, *Runner) {
	t.Helper()
	repository := store.NewMemoryRepository()
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(repository, evidenceStore, 24*time.Hour, slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)))
	return repository, evidenceStore, runner
}

// seedExpired stores an evidence object and its metadata with a retention
// window that already elapsed.
func seedExpired(t *testing.T, repository store.Repository, evidenceStore evidence.Store, content string) domain.Evidence {
	t.Helper()
	item, err := evidenceStore.Put(testTenant, "asset", "job", "page.html", "text/html", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Hour)
	item.RetainUntil = &past
	if err := repository.CreateEvidence(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	return item
}

func TestDryRunRemovesNothing(t *testing.T) {
	repository, evidenceStore, runner := newTestRunner(t)
	item := seedExpired(t, repository, evidenceStore, "expired")

	result, err := runner.RunTenant(context.Background(), testTenant, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || result.ExpiredEvidence != 1 || result.DeletedEvidence != 0 {
		t.Fatalf("unexpected dry run result: %+v", result)
	}
	if _, err := repository.GetEvidence(context.Background(), testTenant, item.ID); err != nil {
		t.Fatalf("dry run removed evidence metadata: %v", err)
	}
	reader, err := evidenceStore.Open(context.Background(), item.ObjectKey)
	if err != nil {
		t.Fatalf("dry run removed the evidence object: %v", err)
	}
	// Windows refuses to remove a directory holding an open handle, so close
	// before TempDir cleanup runs.
	_ = reader.Close()
}

func TestExecuteDeletesExpiredEvidence(t *testing.T) {
	repository, evidenceStore, runner := newTestRunner(t)
	item := seedExpired(t, repository, evidenceStore, "expired")

	result, err := runner.RunTenant(context.Background(), testTenant, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedEvidence != 1 || result.FailedEvidence != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if _, err := repository.GetEvidence(context.Background(), testTenant, item.ID); err != store.ErrNotFound {
		t.Fatalf("metadata survived the pass: %v", err)
	}
	if _, err := evidenceStore.Open(context.Background(), item.ObjectKey); err == nil {
		t.Fatal("evidence object survived the pass")
	}
}

// A referenced evidence object is the basis of an incident conclusion, so
// retention must leave it alone even after its window closes.
func TestReferencedEvidenceIsProtected(t *testing.T) {
	repository, evidenceStore, runner := newTestRunner(t)
	item := seedExpired(t, repository, evidenceStore, "referenced")

	now := time.Now().UTC()
	fingerprint := make([]byte, 64)
	for index := range fingerprint {
		fingerprint[index] = 'a'
	}
	baseline := domain.BaselineVersion{
		ID: "baseline", TenantID: testTenant, AssetID: "asset", Version: 1,
		Status: domain.BaselineApproved, CreatedBy: "tester", CreatedAt: now,
	}
	if err := repository.CreateBaseline(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}
	incident := domain.Incident{
		ID: "incident", TenantID: testTenant, AssetID: "asset",
		BaselineID: baseline.ID, BaselineVersion: 1, Fingerprint: string(fingerprint),
		Severity: domain.SeverityHigh, Status: domain.IncidentOpen, Title: "changed",
		EvidenceIDs: []string{item.ID}, CreatedAt: now, FirstSeenAt: now, LastSeenAt: now,
	}
	if _, _, err := repository.UpsertIncident(context.Background(), incident); err != nil {
		t.Fatal(err)
	}

	result, err := runner.RunTenant(context.Background(), testTenant, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.ProtectedEvidence != 1 || result.DeletedEvidence != 0 {
		t.Fatalf("referenced evidence was not protected: %+v", result)
	}
	if _, err := repository.GetEvidence(context.Background(), testTenant, item.ID); err != nil {
		t.Fatalf("protected evidence was removed: %v", err)
	}
}

// RunAllTenants must cover enabled tenants and skip disabled ones, whose
// evidence is being held for human review.
func TestRunAllTenantsSkipsDisabledTenants(t *testing.T) {
	repository, evidenceStore, runner := newTestRunner(t)
	disabledTenant := "00000000-0000-4000-8000-0000000000bb"
	if err := repository.CreateTenant(context.Background(), domain.Tenant{
		ID: disabledTenant, Slug: "parked", Name: "Parked", Disabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	active := seedExpired(t, repository, evidenceStore, "active")
	parked, err := evidenceStore.Put(disabledTenant, "asset", "job", "page.html", "text/html", []byte("parked"))
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Hour)
	parked.RetainUntil = &past
	if err := repository.CreateEvidence(context.Background(), parked); err != nil {
		t.Fatal(err)
	}

	results := runner.RunAllTenants(context.Background(), true)
	if _, ok := results[disabledTenant]; ok {
		t.Fatal("disabled tenant was swept")
	}
	if _, err := repository.GetEvidence(context.Background(), testTenant, active.ID); err != store.ErrNotFound {
		t.Fatalf("active tenant evidence survived: %v", err)
	}
	if _, err := repository.GetEvidence(context.Background(), disabledTenant, parked.ID); err != nil {
		t.Fatalf("disabled tenant evidence was removed: %v", err)
	}
}
