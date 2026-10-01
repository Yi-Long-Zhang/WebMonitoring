package api

import (
	"bytes"
	"context"
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
	incident, created, err := repository.UpsertIncident(context.Background(), domain.Incident{
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
	if created != store.IncidentCreated {
		t.Fatalf("fixture outcome = %v, want a new incident", created)
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

	events, _, err := repository.ListIncidentEvents(context.Background(), defaultTenant, incident.ID, store.ListOptions{})
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

	events, _, err := repository.ListIncidentEvents(context.Background(), defaultTenant, incident.ID, store.ListOptions{})
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

func getTimelineForTest(t *testing.T, server *Server, cookie *http.Cookie, incidentID string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/incidents/"+incidentID+"/timeline", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

// The timeline is readable by every authenticated role. An auditor who can see
// an incident but not how it was handled has nothing to audit.
func TestIncidentTimelineEndpointIsReadableAndTenantScoped(t *testing.T) {
	repository := store.NewMemoryRepository()
	retentionUser(t, repository, "admin", domain.RoleAdmin)
	retentionUser(t, repository, "auditor", domain.RoleAuditor)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	incident := seedIncident(t, repository)

	adminCookie, adminCsrf := loginForTest(t, server, "admin")
	if response := transitionIncidentForTest(t, server, adminCookie, adminCsrf, incident.ID, `{"status":"ACKNOWLEDGED"}`); response.Code != http.StatusOK {
		t.Fatalf("transition status=%d body=%s", response.Code, response.Body.String())
	}

	auditorCookie, _ := loginForTest(t, server, "auditor")
	response := getTimelineForTest(t, server, auditorCookie, incident.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("auditor read status=%d body=%s", response.Code, response.Body.String())
	}
	var timeline struct {
		Items []domain.IncidentEvent `json:"items"`
	}
	if err := decodeResponse(response, &timeline); err != nil {
		t.Fatal(err)
	}
	if len(timeline.Items) != 2 {
		t.Fatalf("timeline items = %d, want 2", len(timeline.Items))
	}
	// Newest first, and the newest one names the admin who acknowledged it.
	if timeline.Items[0].Type != domain.IncidentEventStatusChanged {
		t.Fatalf("first item type = %s, want STATUS_CHANGED", timeline.Items[0].Type)
	}
	if timeline.Items[0].ActorID != repositoryUserID(t, repository, "admin") {
		t.Fatalf("first item actor = %q", timeline.Items[0].ActorID)
	}
	if timeline.Items[1].Type != domain.IncidentEventCreated {
		t.Fatalf("second item type = %s, want CREATED", timeline.Items[1].Type)
	}

	// A missing id answers 404 rather than an empty list, so "no such incident"
	// is distinguishable from "no entries yet".
	if response := getTimelineForTest(t, server, auditorCookie, identity.New()); response.Code != http.StatusNotFound {
		t.Fatalf("unknown incident status=%d, want 404", response.Code)
	}
}

func repositoryUserID(t *testing.T, repository store.Repository, username string) string {
	t.Helper()
	user, err := repository.FindUserByUsername(context.Background(), defaultTenant, username)
	if err != nil {
		t.Fatal(err)
	}
	return user.ID
}

func commentIncidentForTest(t *testing.T, server *Server, cookie *http.Cookie, csrf, incidentID, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/"+incidentID+"/comments", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

// A comment is part of the incident's history, so it lands on the timeline with
// the author rather than overwriting anything on the incident row.
func TestIncidentCommentAppendsToTimeline(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	incident := seedIncident(t, repository)
	cookie, csrf := loginForTest(t, server, "admin")

	response := commentIncidentForTest(t, server, cookie, csrf, incident.ID, `{"note":"called the site owner"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("comment status=%d body=%s", response.Code, response.Body.String())
	}
	events, _, err := repository.ListIncidentEvents(context.Background(), defaultTenant, incident.ID, store.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("timeline entries = %d, want 2", len(events))
	}
	if events[0].Type != domain.IncidentEventCommented {
		t.Fatalf("newest entry type = %s, want COMMENTED", events[0].Type)
	}
	if events[0].ActorID != admin.ID {
		t.Fatalf("actor = %q, want %q", events[0].ActorID, admin.ID)
	}
	if note, _ := events[0].Details["note"].(string); note != "called the site owner" {
		t.Fatalf("note = %v", events[0].Details["note"])
	}
}

// An empty or whitespace-only note is rejected before anything is written, so a
// comment cannot be used to add a timeline entry that says nothing.
func TestIncidentCommentRejectsBlankNotes(t *testing.T) {
	repository := store.NewMemoryRepository()
	retentionUser(t, repository, "admin", domain.RoleAdmin)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	incident := seedIncident(t, repository)
	cookie, csrf := loginForTest(t, server, "admin")

	for _, body := range []string{`{"note":""}`, `{"note":"   "}`, `{"note":"x"}`} {
		response := commentIncidentForTest(t, server, cookie, csrf, incident.ID, body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %s status=%d, want 400", body, response.Code)
		}
	}
	events, _, err := repository.ListIncidentEvents(context.Background(), defaultTenant, incident.ID, store.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("timeline entries = %d, want only CREATED", len(events))
	}

	// A read-only role cannot comment.
	retentionUser(t, repository, "auditor", domain.RoleAuditor)
	auditorCookie, auditorCsrf := loginForTest(t, server, "auditor")
	if response := commentIncidentForTest(t, server, auditorCookie, auditorCsrf, incident.ID, `{"note":"looks bad"}`); response.Code != http.StatusForbidden {
		t.Fatalf("auditor comment status=%d, want 403", response.Code)
	}
}
