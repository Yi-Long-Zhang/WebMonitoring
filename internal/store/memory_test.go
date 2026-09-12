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
	items := repository.ListAlertDeliveries("tenant", "incident")
	if len(items) != 1 || items[0].ID != first.ID {
		t.Fatalf("unexpected deliveries: %#v", items)
	}
}
