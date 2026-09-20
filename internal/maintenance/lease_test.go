package maintenance

import (
	"context"
	"testing"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/store"
)

// stubLease lets a test decide whether another replica already holds the sweep.
type stubLease struct {
	held     bool
	acquires int
	releases int
}

func (l *stubLease) TryAcquire(context.Context, string) (func(), bool, error) {
	l.acquires++
	if l.held {
		return nil, false, nil
	}
	return func() { l.releases++ }, true, nil
}

// RunDaily runs a full pass immediately at startup, so without a lease every
// replica restart starts its own sweep over the same evidence. The replica that
// loses must skip without touching anything.
func TestRunAllTenantsSkipsWhenAnotherReplicaHoldsTheLease(t *testing.T) {
	repository, evidenceStore, runner := newTestRunner(t)
	item := seedExpired(t, repository, evidenceStore, "expired")
	runner.SetLease(&stubLease{held: true})

	results := runner.RunAllTenants(context.Background(), true)
	if len(results) != 0 {
		t.Fatalf("swept while the lease was held elsewhere: %+v", results)
	}
	if _, err := repository.GetEvidence(testTenant, item.ID); err != nil {
		t.Fatalf("evidence was deleted without the lease: %v", err)
	}
}

func TestRunAllTenantsReleasesTheLease(t *testing.T) {
	repository, evidenceStore, runner := newTestRunner(t)
	seedExpired(t, repository, evidenceStore, "expired")
	lease := &stubLease{}
	runner.SetLease(lease)

	results := runner.RunAllTenants(context.Background(), true)
	if len(results) != 1 {
		t.Fatalf("unexpected results: %+v", results)
	}
	if lease.acquires != 1 || lease.releases != 1 {
		t.Fatalf("acquires=%d releases=%d, want exactly one of each", lease.acquires, lease.releases)
	}
}

func scheduledAudits(t *testing.T, repository store.Repository, tenantID string) []domain.AuditEvent {
	t.Helper()
	events, _, err := repository.ListAudits(tenantID, store.AuditFilter{}, store.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var matched []domain.AuditEvent
	for _, event := range events {
		if event.Action == "maintenance.retention.scheduled" {
			matched = append(matched, event)
		}
	}
	return matched
}

// The scheduled sweep has no request and no named actor, so it has to write its
// own audit record. Evidence destroyed by an automated run with no trace
// contradicts the rule that deletions must be auditable, and it is exactly the
// case where no human authorized the specific deletion.
func TestScheduledSweepAuditsWhatItDeleted(t *testing.T) {
	repository, evidenceStore, runner := newTestRunner(t)
	seedExpired(t, repository, evidenceStore, "expired")

	results := runner.RunAllTenants(context.Background(), true)
	if result, ok := results[testTenant]; !ok || result.DeletedEvidence != 1 {
		t.Fatalf("unexpected results: %+v", results)
	}

	audits := scheduledAudits(t, repository, testTenant)
	if len(audits) != 1 {
		t.Fatalf("scheduled audit events = %d, want 1", len(audits))
	}
	if audits[0].Result != "SUCCESS" {
		t.Fatalf("result=%q, want SUCCESS", audits[0].Result)
	}
	if audits[0].ActorID != "" {
		t.Fatalf("recorded actor %q for a run no human triggered", audits[0].ActorID)
	}
	if deleted, ok := audits[0].Details["deletedEvidence"].(int); !ok || deleted != 1 {
		t.Fatalf("audit does not record the deletion: %+v", audits[0].Details)
	}
}

// A sweep that found nothing to do must stay out of the audit log, or a daily
// pass over every tenant would bury the entries that matter.
func TestScheduledSweepAuditsNothingWhenNothingChanged(t *testing.T) {
	repository, _, runner := newTestRunner(t)
	if results := runner.RunAllTenants(context.Background(), true); len(results) != 1 {
		t.Fatalf("unexpected results: %+v", results)
	}
	if audits := scheduledAudits(t, repository, testTenant); len(audits) != 0 {
		t.Fatalf("idle sweep wrote %d audit events", len(audits))
	}
}

// A dry run must never leave an audit record claiming work happened.
func TestDryRunSweepWritesNoAudit(t *testing.T) {
	repository, evidenceStore, runner := newTestRunner(t)
	seedExpired(t, repository, evidenceStore, "expired")

	if results := runner.RunAllTenants(context.Background(), false); len(results) != 1 {
		t.Fatalf("unexpected results: %+v", results)
	}
	if audits := scheduledAudits(t, repository, testTenant); len(audits) != 0 {
		t.Fatalf("dry run wrote %d audit events", len(audits))
	}
}
