package store

import (
	"testing"
	"time"

	"webmonitoring/internal/domain"
)

func TestUpsertIncidentDeduplicatesActiveEvents(t *testing.T) {
	repository := NewMemoryRepository()
	now := time.Now().UTC()
	first := domain.Incident{
		ID: "first", TenantID: "tenant", AssetID: "asset", BaselineVersion: 1,
		Fingerprint: "same", Severity: domain.SeverityHigh, Status: domain.IncidentOpen,
		Title: "event", OccurrenceCount: 1, FirstSeenAt: now, LastSeenAt: now,
	}
	saved, created, err := repository.UpsertIncident(first)
	if err != nil || !created || saved.OccurrenceCount != 1 {
		t.Fatalf("create incident: %#v %v %v", saved, created, err)
	}
	second := first
	second.ID = "second"
	second.LastSeenAt = now.Add(time.Minute)
	saved, created, err = repository.UpsertIncident(second)
	if err != nil || created || saved.ID != "first" || saved.OccurrenceCount != 2 {
		t.Fatalf("deduplicate incident: %#v %v %v", saved, created, err)
	}
}

func TestClaimJobOnlySucceedsOnce(t *testing.T) {
	repository := NewMemoryRepository()
	job := domain.DetectionJob{ID: "job", TenantID: "tenant", AssetID: "asset", Mode: "HTTP", Status: domain.JobQueued, CreatedAt: time.Now().UTC()}
	if err := repository.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	claimed, err := repository.ClaimJob("tenant", "job", started)
	if err != nil || !claimed {
		t.Fatalf("first claim: %v %v", claimed, err)
	}
	claimed, err = repository.ClaimJob("tenant", "job", started)
	if err != nil || claimed {
		t.Fatalf("duplicate claim: %v %v", claimed, err)
	}
	stored, err := repository.GetJob("tenant", "job")
	if err != nil || stored.Status != domain.JobRunning || stored.StartedAt == nil {
		t.Fatalf("stored claim: %#v %v", stored, err)
	}
}

func TestClaimJobRecoversOnlyAfterLeaseExpires(t *testing.T) {
	repository := NewMemoryRepository()
	started := time.Now().UTC().Add(-6 * time.Minute)
	job := domain.DetectionJob{
		ID: "job", TenantID: "tenant", AssetID: "asset", Mode: "HTTP",
		Status: domain.JobRunning, StartedAt: &started, CreatedAt: started,
	}
	if err := repository.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.ClaimJob("tenant", "job", time.Now().UTC())
	if err != nil || !claimed {
		t.Fatalf("stale running job was not reclaimed: claimed=%v err=%v", claimed, err)
	}

	claimed, err = repository.ClaimJob("tenant", "job", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("active running job was reclaimed before its lease expired")
	}
}

func TestAlertDeliveryDeduplicatesByIncidentAndChannel(t *testing.T) {
	repository := NewMemoryRepository()
	first := domain.AlertDelivery{ID: "delivery-1", TenantID: "tenant", IncidentID: "incident", ChannelID: "channel", DedupKey: "incident:channel", Status: "PENDING", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := repository.CreateAlertDelivery(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "delivery-2"
	if err := repository.CreateAlertDelivery(second); err != ErrConflict {
		t.Fatalf("expected delivery conflict, got %v", err)
	}
	items, err := repository.ListAlertDeliveries("tenant", "incident")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != first.ID {
		t.Fatalf("unexpected deliveries: %#v", items)
	}
}

func TestEvidenceRetentionProtectsIncidentReferences(t *testing.T) {
	repository := NewMemoryRepository()
	expired := time.Now().UTC().Add(-time.Hour)
	item := domain.Evidence{
		ID: "evidence", TenantID: "tenant", AssetID: "asset", JobID: "job",
		CreatedAt: time.Now().UTC().Add(-24 * time.Hour), RetainUntil: &expired,
	}
	if err := repository.CreateEvidence(item); err != nil {
		t.Fatal(err)
	}
	items, err := repository.ListExpiredEvidence("tenant", time.Now().UTC())
	if err != nil || len(items) != 1 {
		t.Fatalf("unexpected expired evidence: %#v err=%v", items, err)
	}
	if _, _, err := repository.UpsertIncident(domain.Incident{
		ID: "incident", TenantID: "tenant", AssetID: "asset", BaselineID: "baseline",
		BaselineVersion: 1, Fingerprint: "fingerprint", Status: domain.IncidentOpen,
		EvidenceIDs: []string{item.ID}, CreatedAt: time.Now().UTC(),
		FirstSeenAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	referenced, err := repository.IsEvidenceReferenced("tenant", item.ID)
	if err != nil || !referenced {
		t.Fatalf("reference check=%v err=%v", referenced, err)
	}
	if err := repository.DeleteEvidence("tenant", item.ID); err != ErrConflict {
		t.Fatalf("expected retention conflict, got %v", err)
	}
}

func TestAuditRetentionDeletesOnlyExpiredTenantEvents(t *testing.T) {
	repository := NewMemoryRepository()
	cutoff := time.Now().UTC()
	for _, event := range []domain.AuditEvent{
		{ID: "expired", TenantID: "tenant", CreatedAt: cutoff.Add(-time.Hour)},
		{ID: "current", TenantID: "tenant", CreatedAt: cutoff.Add(time.Hour)},
		{ID: "other", TenantID: "other", CreatedAt: cutoff.Add(-time.Hour)},
	} {
		if err := repository.AppendAudit(event); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := repository.DeleteAuditsBefore("tenant", cutoff)
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	current, _ := repository.ListAudits("tenant")
	other, _ := repository.ListAudits("other")
	if len(current) != 1 || current[0].ID != "current" || len(other) != 1 {
		t.Fatalf("unexpected retained audits: current=%#v other=%#v", current, other)
	}
}
