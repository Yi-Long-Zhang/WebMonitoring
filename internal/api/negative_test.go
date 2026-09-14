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

func TestUnknownTenantSlugRejectedOnLogin(t *testing.T) {
	repository := store.NewMemoryRepository()
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session",
		bytes.NewBufferString(`{"tenant":"missing","username":"admin","password":"whatever"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unknown slug status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestNonSuperAdminCannotAccessTenants(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	cookie, _ := loginForTest(t, server, admin.Username)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tenants", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("non-super-admin tenants status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSuperAdminCreatesTenantAndSeedsAdmin(t *testing.T) {
	repository := store.NewMemoryRepository()
	root := retentionUser(t, repository, "root", domain.RoleSuperAdmin)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	cookie, csrf := loginForTest(t, server, root.Username)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/tenants",
		bytes.NewBufferString(`{"name":"Acme Corp","slug":"acme","adminUsername":"acme-admin","adminPassword":"a-long-password-for-testing"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create tenant status=%d body=%s", response.Code, response.Body.String())
	}
	var tenant domain.Tenant
	if err := json.NewDecoder(response.Body).Decode(&tenant); err != nil {
		t.Fatal(err)
	}
	if tenant.Slug != "acme" {
		t.Fatalf("unexpected tenant: %#v", tenant)
	}
	if _, err := repository.FindUserByUsername(tenant.ID, "acme-admin"); err != nil {
		t.Fatalf("seeded admin missing: %v", err)
	}

	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session",
		bytes.NewBufferString(`{"tenant":"acme","username":"acme-admin","password":"a-long-password-for-testing"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	server.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusCreated {
		t.Fatalf("seeded admin login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
}

func TestCrossTenantSiteNotVisible(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	now := time.Now().UTC()
	other := domain.Site{ID: "other-site", TenantID: "other-tenant", Name: "Other", Domain: "example.org", OwnerID: admin.ID, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateSite(other); err != nil {
		t.Fatal(err)
	}
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	cookie, _ := loginForTest(t, server, admin.Username)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/sites/other-site", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant site status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAssetCannotReferenceForeignSite(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	now := time.Now().UTC()
	foreign := domain.Site{ID: "foreign-site", TenantID: "other-tenant", Name: "Foreign", Domain: "example.org", OwnerID: admin.ID, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateSite(foreign); err != nil {
		t.Fatal(err)
	}
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	cookie, csrf := loginForTest(t, server, admin.Username)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/assets",
		bytes.NewBufferString(`{"name":"asset","url":"https://example.com","type":"OFFICIAL","siteId":"foreign-site"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("foreign site status=%d body=%s", response.Code, response.Body.String())
	}
}
