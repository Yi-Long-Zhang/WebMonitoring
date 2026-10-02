package store

import (
	"context"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
)

// The stamp is claimed before anything is sent, so two sweeps racing over the
// same incident produce one notice rather than two.
func TestEscalationStampIsExclusive(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			tenantID, assetID, baselineID := seedIncidentFixture(t, repository)
			incident, _, err := repository.UpsertIncident(context.Background(), domain.Incident{
				ID: identity.New(), TenantID: tenantID, AssetID: assetID, BaselineID: baselineID,
				BaselineVersion: 1, Fingerprint: "exclusive-fingerprint", Severity: domain.SeverityCritical,
				Status: domain.IncidentOpen, Title: "t", Signals: []domain.Signal{signal("tamper")},
				OccurrenceCount: 1, CreatedAt: time.Now().UTC(), FirstSeenAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(),
			})
			if err != nil {
				t.Fatal(err)
			}
			first, err := repository.MarkIncidentEscalated(context.Background(), tenantID, incident.ID, time.Now().UTC())
			if err != nil || !first {
				t.Fatalf("first stamp = %v err=%v, want true", first, err)
			}
			second, err := repository.MarkIncidentEscalated(context.Background(), tenantID, incident.ID, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			if second {
				t.Fatal("the second stamp won, so two sweeps would both notify")
			}
		})
	}
}
