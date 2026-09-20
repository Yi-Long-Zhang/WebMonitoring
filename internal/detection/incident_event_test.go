package detection

import (
	"testing"
	"time"

	"webmonitoring/internal/domain"
)

// One disposition request produces one timeline entry, with the owner and the
// resolution carried in Details rather than as separate rows: entries written at
// the same instant have no defined order between them, so splitting a single
// user action into three would make the timeline ambiguous.
func TestIncidentTransitionEventFoldsOneActionIntoOneEntry(t *testing.T) {
	at := time.Now().UTC()
	actor := domain.User{ID: "user-1", TenantID: "tenant-1", Username: "admin"}
	before := domain.Incident{
		ID: "incident-1", TenantID: "tenant-1", Status: domain.IncidentOpen,
		Severity: domain.SeverityHigh, OwnerID: "",
	}

	cases := map[string]struct {
		after          domain.Incident
		wantOwner      bool
		wantResolution bool
	}{
		"status only": {
			after: domain.Incident{ID: "incident-1", TenantID: "tenant-1", Status: domain.IncidentAcknowledged, Severity: domain.SeverityHigh},
		},
		"owner assigned": {
			after:     domain.Incident{ID: "incident-1", TenantID: "tenant-1", Status: domain.IncidentAcknowledged, Severity: domain.SeverityHigh, OwnerID: "user-9"},
			wantOwner: true,
		},
		"resolution recorded": {
			after:          domain.Incident{ID: "incident-1", TenantID: "tenant-1", Status: domain.IncidentAcknowledged, Severity: domain.SeverityHigh, Resolution: "rolled back the deploy"},
			wantResolution: true,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			event := IncidentTransitionEvent(before, testCase.after, actor, at)
			if event.Type != domain.IncidentEventStatusChanged {
				t.Fatalf("type = %s, want STATUS_CHANGED", event.Type)
			}
			if event.IncidentID != before.ID || event.TenantID != before.TenantID {
				t.Fatalf("event points at %s/%s", event.TenantID, event.IncidentID)
			}
			if event.ActorID != actor.ID {
				t.Fatalf("actor = %q, want %q", event.ActorID, actor.ID)
			}
			if event.FromStatus != before.Status || event.ToStatus != testCase.after.Status {
				t.Fatalf("statuses = %s -> %s, want %s -> %s", event.FromStatus, event.ToStatus, before.Status, testCase.after.Status)
			}
			if !event.CreatedAt.Equal(at) {
				t.Fatalf("created at %s, want %s", event.CreatedAt, at)
			}
			if _, ok := event.Details["ownerId"]; ok != testCase.wantOwner {
				t.Fatalf("ownerId present = %v, want %v (%+v)", ok, testCase.wantOwner, event.Details)
			}
			if _, ok := event.Details["resolution"]; ok != testCase.wantResolution {
				t.Fatalf("resolution present = %v, want %v (%+v)", ok, testCase.wantResolution, event.Details)
			}
		})
	}
}

// A resolution that was already recorded must not be re-recorded when the same
// value is carried through a later transition.
func TestIncidentTransitionEventSkipsUnchangedFields(t *testing.T) {
	actor := domain.User{ID: "user-1", TenantID: "tenant-1"}
	before := domain.Incident{
		ID: "incident-1", TenantID: "tenant-1", Status: domain.IncidentInvestigating,
		Severity: domain.SeverityHigh, OwnerID: "user-9", Resolution: "triaged",
	}
	after := before
	after.Status = domain.IncidentResolved

	event := IncidentTransitionEvent(before, after, actor, time.Now().UTC())
	if len(event.Details) != 0 {
		t.Fatalf("details = %+v, want none for an unchanged owner and resolution", event.Details)
	}
}
