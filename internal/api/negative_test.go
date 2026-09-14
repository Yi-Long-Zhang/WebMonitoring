package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/store"
)

func TestAuditorCannotWriteAndUnverifiedAssetCannotRun(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	auditor := retentionUser(t, repository, "auditor", domain.RoleAuditor)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)

	auditorCookie, auditorCSRF := loginForTest(t, server, auditor.Username)
	write := httptest.NewRequest(http.MethodPost, "/api/v1/assets",
		bytes.NewBufferString(`{"name":"blocked","url":"https://example.com","type":"OFFICIAL"}`))
	write.Header.Set("Content-Type", "application/json")
	write.Header.Set("X-CSRF-Token", auditorCSRF)
	write.AddCookie(auditorCookie)
	writeResponse := httptest.NewRecorder()
	server.ServeHTTP(writeResponse, write)
	if writeResponse.Code != http.StatusForbidden {
		t.Fatalf("auditor write status=%d body=%s", writeResponse.Code, writeResponse.Body.String())
	}

	adminCookie, adminCSRF := loginForTest(t, server, admin.Username)
	create := httptest.NewRequest(http.MethodPost, "/api/v1/assets",
		bytes.NewBufferString(`{"name":"unverified","url":"https://example.com","type":"OFFICIAL"}`))
	create.Header.Set("Content-Type", "application/json")
	create.Header.Set("X-CSRF-Token", adminCSRF)
	create.AddCookie(adminCookie)
	createResponse := httptest.NewRecorder()
	server.ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("asset create status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}
	var asset domain.Asset
	if err := json.NewDecoder(createResponse.Body).Decode(&asset); err != nil {
		t.Fatal(err)
	}

	run := httptest.NewRequest(http.MethodPost, "/api/v1/detection-jobs",
		bytes.NewBufferString(`{"assetId":"`+asset.ID+`","mode":"HTTP"}`))
	run.Header.Set("Content-Type", "application/json")
	run.Header.Set("X-CSRF-Token", adminCSRF)
	run.AddCookie(adminCookie)
	runResponse := httptest.NewRecorder()
	server.ServeHTTP(runResponse, run)
	if runResponse.Code != http.StatusConflict {
		t.Fatalf("unverified detection status=%d body=%s", runResponse.Code, runResponse.Body.String())
	}
}

func TestCrossTenantResourceIsNotVisible(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	other := domain.Asset{
		ID: "other-asset", TenantID: "other-tenant", Name: "other", URL: "https://example.org",
		OwnerID: admin.ID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repository.CreateAsset(other); err != nil {
		t.Fatal(err)
	}
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	cookie, _ := loginForTest(t, server, admin.Username)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/assets/"+other.ID, nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestReportRejectsMissingEvidence(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	now := time.Now().UTC()
	if _, _, err := repository.UpsertIncident(domain.Incident{
		ID: "incident", TenantID: defaultTenant, AssetID: "asset", BaselineID: "baseline",
		BaselineVersion: 1, Fingerprint: "fingerprint", Severity: domain.SeverityHigh,
		Status: domain.IncidentOpen, Title: "missing evidence", EvidenceIDs: []string{"missing"},
		CreatedAt: now, FirstSeenAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	cookie, _ := loginForTest(t, server, admin.Username)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/reports/incident", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusFailedDependency {
		t.Fatalf("missing evidence status=%d body=%s", response.Code, response.Body.String())
	}
}
