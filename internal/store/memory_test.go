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
