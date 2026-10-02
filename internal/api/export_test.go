package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"webmonitoring/internal/domain"
)

func getExport(t *testing.T, server *Server, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

// An export goes into a file someone hands to a customer, so the filter has to
// be the listing's filter exactly: a caller who exported a subset they did not
// ask for has no way to notice.
func TestIncidentExportHonoursTheListingFilter(t *testing.T) {
	server, cookie, _, incidents := incidentFilterServer(t, 3)
	closed := incidents[1]
	closed.Status = domain.IncidentFalsePositive
	closed.Severity = domain.SeverityCritical
	if err := server.repository.UpdateIncident(context.Background(), closed); err != nil {
		t.Fatal(err)
	}

	response := getExport(t, server, cookie, "/api/v1/incidents/export?format=csv&status=FALSE_POSITIVE")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/csv") {
		t.Fatalf("content type = %q", contentType)
	}
	if disposition := response.Header().Get("Content-Disposition"); !strings.Contains(disposition, "incidents.csv") {
		t.Fatalf("disposition = %q", disposition)
	}
	records, err := csv.NewReader(strings.NewReader(response.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("csv carried %d lines (header + rows), want 2", len(records))
	}
	if records[0][0] != "id" || records[0][2] != "severity" {
		t.Fatalf("header = %v", records[0])
	}
	if records[1][0] != closed.ID || records[1][3] != "FALSE_POSITIVE" {
		t.Fatalf("the filtered row is %v, want the dismissed incident", records[1])
	}
}

// The audit export takes the audit listing's filter, and the detail map has to
// survive into the file: it is where the action's parameters live.
func TestAuditExportCarriesFiltersAndDetails(t *testing.T) {
	server, cookie, _, _ := incidentFilterServer(t, 1)
	now := time.Now().UTC()
	if err := server.repository.AppendAudit(context.Background(), domain.AuditEvent{
		ID: "11111111-1111-4111-8111-111111111111", TenantID: defaultTenant,
		Action: "asset.create", ResourceType: "asset", ResourceID: "asset-1", Result: "SUCCESS",
		TraceID: "trace-1", Details: map[string]any{"rows": 3}, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.repository.AppendAudit(context.Background(), domain.AuditEvent{
		ID: "22222222-2222-4222-8222-222222222222", TenantID: defaultTenant,
		Action: "tenant.update", ResourceType: "tenant", ResourceID: "tenant-1", Result: "SUCCESS",
		TraceID: "trace-2", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	response := getExport(t, server, cookie, "/api/v1/audit-events/export?format=csv&action=asset.create")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	records, err := csv.NewReader(strings.NewReader(response.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("csv carried %d lines, want the header and the one matching action", len(records))
	}
	if records[1][2] != "asset.create" {
		t.Fatalf("row = %v, want the filtered action", records[1])
	}
	if !strings.Contains(records[1][7], `"rows":3`) {
		t.Fatalf("the detail map did not survive into the file: %q", records[1][7])
	}
}

// A format the caller asked for and did not get must not be silently swapped for
// one they did.
func TestExportRejectsAnUnknownFormat(t *testing.T) {
	server, cookie, _, _ := incidentFilterServer(t, 1)
	for _, path := range []string{"/api/v1/incidents/export?format=xlsx", "/api/v1/audit-events/export?format=pdf"} {
		response := getExport(t, server, cookie, path)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d, want 400", path, response.Code)
		}
	}
}

// JSON stays the default, matching the asset export.
func TestExportDefaultsToJSON(t *testing.T) {
	server, cookie, _, incidents := incidentFilterServer(t, 2)
	response := getExport(t, server, cookie, "/api/v1/incidents/export")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	var body struct {
		Items []domain.Incident `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != len(incidents) {
		t.Fatalf("exported %d incidents, want %d", len(body.Items), len(incidents))
	}
}
