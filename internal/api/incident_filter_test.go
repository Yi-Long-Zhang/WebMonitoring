package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// incidentFilterServer builds a server holding `count` incidents with distinct
// fingerprints, so a listing can be narrowed without the fixtures colliding on
// the active-fingerprint index.
func incidentFilterServer(t *testing.T, count int) (*Server, *http.Cookie, string, []domain.Incident) {
	t.Helper()
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	incidents := make([]domain.Incident, 0, count)
	now := time.Now().UTC()
	for index := range count {
		incident, _, err := repository.UpsertIncident(context.Background(), domain.Incident{
			ID: identity.New(), TenantID: defaultTenant, AssetID: "asset-1",
			BaselineID: "baseline-1", BaselineVersion: 1,
			Fingerprint: fmt.Sprintf("filter-fingerprint-%02d", index),
			Severity:    domain.SeverityHigh, Status: domain.IncidentOpen,
			Title:       fmt.Sprintf("changed %d", index),
			Signals:     []domain.Signal{{Kind: "content", Severity: domain.SeverityHigh, Summary: "changed", RuleVersion: "v1", Fingerprint: "s"}},
			CreatedAt:   now.Add(-time.Duration(index) * time.Minute),
			FirstSeenAt: now.Add(-time.Duration(index) * time.Minute),
			LastSeenAt:  now.Add(-time.Duration(index) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		incidents = append(incidents, incident)
	}
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	cookie, csrf := loginForTest(t, server, admin.Username)
	return server, cookie, csrf, incidents
}

func getIncidents(t *testing.T, server *Server, cookie *http.Cookie, query string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/incidents"+query, nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

// A malformed filter is a 400. Letting it through would reach Postgres as a bad
// uuid cast (a 500) or silently return a subset the caller did not ask for.
func TestIncidentFilterRejectsMalformedInput(t *testing.T) {
	server, cookie, _, _ := incidentFilterServer(t, 1)

	cases := []struct {
		name  string
		query string
	}{
		{"status is unknown", "?status=ARCHIVED"},
		{"status is lower case", "?status=open"},
		{"severity is unknown", "?severity=URGENT"},
		{"severity is lower case", "?severity=high"},
		{"assetId is not a uuid", "?assetId=asset-1"},
		{"ownerId is not a uuid", "?ownerId=me"},
		{"from is not RFC3339", "?from=yesterday"},
		{"to is not RFC3339", "?to=2026-13-45"},
		{"to precedes from", "?from=2026-02-01T00:00:00Z&to=2026-01-01T00:00:00Z"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := getIncidents(t, server, cookie, testCase.query)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != "INVALID_FILTER" {
				t.Fatalf("code=%q body=%s", body.Code, response.Body.String())
			}
		})
	}
}

// A well-formed filter narrows the listing, and the cursor it returns keeps
// narrowing: a filter that applied to the first page but not the second would
// leak rows the caller excluded.
func TestIncidentFilterNarrowsListingAndSurvivesCursor(t *testing.T) {
	server, cookie, _, incidents := incidentFilterServer(t, 3)
	// One of the three is dismissed, so a status filter has something to exclude.
	closed := incidents[2]
	closed.Status = domain.IncidentFalsePositive
	if err := server.repository.UpdateIncident(context.Background(), closed); err != nil {
		t.Fatal(err)
	}

	response := getIncidents(t, server, cookie, "?limit=1&status=OPEN")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var page struct {
		Items      []domain.Incident `json:"items"`
		NextCursor string            `json:"nextCursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("first page = %+v, want one incident and a cursor", page)
	}

	second := getIncidents(t, server, cookie, "?limit=1&status=OPEN&cursor="+page.NextCursor)
	if second.Code != http.StatusOK {
		t.Fatalf("second page status=%d body=%s", second.Code, second.Body.String())
	}
	var next struct {
		Items      []domain.Incident `json:"items"`
		NextCursor string            `json:"nextCursor"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &next); err != nil {
		t.Fatal(err)
	}
	if len(next.Items) != 1 {
		t.Fatalf("second page carried %d incidents, want the second OPEN one", len(next.Items))
	}
	if next.Items[0].ID == page.Items[0].ID {
		t.Fatal("the cursor returned the same incident twice")
	}
	if next.Items[0].Status != domain.IncidentOpen {
		t.Fatalf("second page leaked status %s past the filter", next.Items[0].Status)
	}
}

func postBatchTransition(t *testing.T, server *Server, cookie *http.Cookie, csrf, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/batch-transition", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

type batchRow struct {
	Index      int    `json:"index"`
	IncidentID string `json:"incidentId"`
	Status     string `json:"status"`
	Code       string `json:"code"`
}

type batchResult struct {
	Requested int        `json:"requested"`
	Updated   int        `json:"updated"`
	Results   []batchRow `json:"results"`
}

// A batch over mixed states has to report per row. The transitions are
// state-dependent, so "some of these cannot move" is the normal case rather than
// an error, and a bare 409 would leave the caller unable to tell which rows did.
func TestBatchTransitionReportsPerRowOutcome(t *testing.T) {
	server, cookie, csrf, incidents := incidentFilterServer(t, 3)
	closed := incidents[1]
	closed.Status = domain.IncidentFalsePositive
	if err := server.repository.UpdateIncident(context.Background(), closed); err != nil {
		t.Fatal(err)
	}
	unknownID := identity.New()

	body := fmt.Sprintf(`{"incidentIds":[%q,%q,%q,%q],"status":"ACKNOWLEDGED"}`,
		incidents[0].ID, closed.ID, unknownID, incidents[2].ID)
	response := postBatchTransition(t, server, cookie, csrf, body)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result batchResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Requested != 4 || result.Updated != 2 {
		t.Fatalf("requested=%d updated=%d, want 4 and 2", result.Requested, result.Updated)
	}
	codes := map[string]string{}
	for _, row := range result.Results {
		codes[row.IncidentID] = row.Status + "/" + row.Code
	}
	if got := codes[incidents[0].ID]; got != "UPDATED/" {
		t.Fatalf("open incident = %q, want UPDATED", got)
	}
	if got := codes[incidents[2].ID]; got != "UPDATED/" {
		t.Fatalf("second open incident = %q, want UPDATED", got)
	}
	if got := codes[closed.ID]; !strings.HasPrefix(got, "REJECTED/INVALID_INCIDENT_TRANSITION") {
		t.Fatalf("dismissed incident = %q, want a rejected transition", got)
	}
	if got := codes[unknownID]; !strings.HasPrefix(got, "REJECTED/INCIDENT_NOT_FOUND") {
		t.Fatalf("unknown incident = %q, want INCIDENT_NOT_FOUND", got)
	}

	// The rows that could move did move, and the one that could not did not.
	reloaded := getIncidents(t, server, cookie, "?status=ACKNOWLEDGED")
	var page struct {
		Items []domain.Incident `json:"items"`
	}
	if err := json.Unmarshal(reloaded.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("acknowledged incidents = %d, want 2", len(page.Items))
	}
}

func TestBatchTransitionRejectsMalformedRequest(t *testing.T) {
	server, cookie, csrf, incidents := incidentFilterServer(t, 1)

	cases := []struct {
		name string
		body string
		code string
	}{
		{"empty list", `{"incidentIds":[],"status":"ACKNOWLEDGED"}`, "INVALID_BATCH"},
		{"unknown status", fmt.Sprintf(`{"incidentIds":[%q],"status":"ARCHIVED"}`, incidents[0].ID), "INVALID_FILTER"},
		{
			"owner from another tenant",
			fmt.Sprintf(`{"incidentIds":[%q],"status":"ACKNOWLEDGED","ownerId":%q}`, incidents[0].ID, identity.New()),
			"INVALID_OWNER",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := postBatchTransition(t, server, cookie, csrf, testCase.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != testCase.code {
				t.Fatalf("code=%q want %q (body %s)", body.Code, testCase.code, response.Body.String())
			}
		})
	}
}

// A batch that would exceed the row limit is refused outright rather than
// truncated: silently acting on part of the request is the failure mode a
// caller cannot see.
func TestBatchTransitionRefusesAnOversizedRequest(t *testing.T) {
	server, cookie, csrf, _ := incidentFilterServer(t, 1)
	ids := make([]string, 0, maxBatchIncidents+1)
	for range maxBatchIncidents + 1 {
		ids = append(ids, identity.New())
	}
	payload, err := json.Marshal(map[string]any{"incidentIds": ids, "status": "ACKNOWLEDGED"})
	if err != nil {
		t.Fatal(err)
	}
	response := postBatchTransition(t, server, cookie, csrf, string(payload))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "BATCH_TOO_LARGE" {
		t.Fatalf("code=%q body=%s", body.Code, response.Body.String())
	}
}
