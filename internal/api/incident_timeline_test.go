package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/store"
)

// seedIncident creates one incident through the repository, which is also what
// writes its CREATED timeline entry.
func seedIncident(t *testing.T, repository store.Repository) domain.Incident {
	t.Helper()
	now := time.Now().UTC()
	incident, created, err := repository.UpsertIncident(domain.Incident{
		ID: identity.New(), TenantID: defaultTenant, AssetID: "asset-1",
		BaselineID: "baseline-1", BaselineVersion: 1, Fingerprint: strings.Repeat("a", 64),
		Severity: domain.SeverityHigh, Status: domain.IncidentOpen, Title: "changed",
		Signals: []domain.Signal{{
			Kind: "content", Severity: domain.SeverityHigh, Summary: "content changed",
			RuleVersion: "v1", Fingerprint: "signal-1",
		}},
		EvidenceIDs: []string{"ev-1"}, OccurrenceCount: 1,
		CreatedAt: now, FirstSeenAt: now, LastSeenAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("fixture did not create a new incident")
	}
	return incident
}

func transitionIncidentForTest(t *testing.T, server *Server, cookie *http.Cookie, csrf, incidentID, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/"+incidentID+"/transition", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

// The status change a person makes has to end up in the timeline, with who did
// it and both ends of the transition.
func TestIncidentTransitionRecordsTimelineEntry(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	incident := seedIncident(t, repository)
	cookie, csrf := loginForTest(t, server, "admin")

	response := transitionIncidentForTest(t, server, cookie, csrf, incident.ID, `{"status":"ACKNOWLEDGED"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("transition status=%d body=%s", response.Code, response.Body.String())
	}

	events, _, err := repository.ListIncidentEvents(defaultTenant, incident.ID, store.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("timeline entries = %d, want 2 (CREATED plus the transition)", len(events))
	}
	entry := events[0]
	if entry.Type != domain.IncidentEventStatusChanged {
		t.Fatalf("newest entry type = %s, want STATUS_CHANGED", entry.Type)
	}
	if entry.ActorID != admin.ID {
		t.Fatalf("actor = %q, want %q", entry.ActorID, admin.ID)
	}
	if entry.FromStatus != domain.IncidentOpen || entry.ToStatus != domain.IncidentAcknowledged {
		t.Fatalf("entry records %s -> %s", entry.FromStatus, entry.ToStatus)
	}
}

// Validation runs before anything is written, so a rejected transition must not
// leave a record claiming the incident moved.
func TestRejectedIncidentTransitionAddsNoTimelineEntry(t *testing.T) {
	repository := store.NewMemoryRepository()
	retentionUser(t, repository, "admin", domain.RoleAdmin)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	incident := seedIncident(t, repository)
	cookie, csrf := loginForTest(t, server, "admin")

	// OPEN -> CLOSED skips the whole investigation, which the state machine
	// rejects.
	response := transitionIncidentForTest(t, server, cookie, csrf, incident.ID, `{"status":"CLOSED","resolution":"nothing to do"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("invalid transition status=%d, want 409; body=%s", response.Code, response.Body.String())
	}

	events, _, err := repository.ListIncidentEvents(defaultTenant, incident.ID, store.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("timeline entries = %d, want only the CREATED entry", len(events))
	}
	if events[0].Type != domain.IncidentEventCreated {
		t.Fatalf("entry type = %s, want CREATED", events[0].Type)
	}
}
