package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

type importResult struct {
	DryRun  bool `json:"dryRun"`
	Rows    int  `json:"rows"`
	Created int  `json:"created"`
	Results []struct {
		Index   int    `json:"index"`
		AssetID string `json:"assetId"`
		Name    string `json:"name"`
		URL     string `json:"url"`
		Status  string `json:"status"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"results"`
}

func TestImportAssetsReportsPartialFailure(t *testing.T) {
	repository, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	cookie, csrf := loginForTest(t, server, admin.Username)

	body := `{"assets":[
		{"name":"good","url":"https://good.example","type":"OFFICIAL"},
		{"name":"bad","url":"ftp://bad.example","type":"OFFICIAL"},
		{"name":"third","url":"https://third.example","type":"BUSINESS"}
	]}`
	response := doJSON(t, server, http.MethodPost, "/api/v1/assets/import", cookie, csrf, body)
	if response.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", response.Code, response.Body.String())
	}
	var result importResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Rows != 3 || result.Created != 2 {
		t.Fatalf("unexpected summary: %+v", result)
	}
	// Statuses are reported per row in the request order.
	wantStatus := []string{"CREATED", "REJECTED", "CREATED"}
	for index, want := range wantStatus {
		if result.Results[index].Status != want {
			t.Fatalf("row %d status=%q want %q", index, result.Results[index].Status, want)
		}
	}
	if result.Results[1].Code != "UNSAFE_TARGET" {
		t.Fatalf("expected UNSAFE_TARGET, got %q", result.Results[1].Code)
	}

	// The rejected row must not exist; the accepted rows must.
	assets, _, err := repository.ListAssets(defaultTenant, store.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	urls := make(map[string]bool, len(assets))
	for _, asset := range assets {
		urls[asset.URL] = true
	}
	if !urls["https://good.example"] || !urls["https://third.example"] || len(assets) != 2 {
		t.Fatalf("unexpected stored assets: %v", urls)
	}
}

func TestImportAssetsRejectsDuplicateAndCrossTenantOwner(t *testing.T) {
	repository, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	// A user in a different tenant, used to prove owner scoping crosses tenants.
	other := retentionUserInTenant(t, repository, "00000000-0000-4000-8000-000000000002", "other", domain.RoleAdmin)

	cookie, csrf := loginForTest(t, server, admin.Username)
	body := `{"assets":[
		{"name":"one","url":"https://dup.example","type":"OFFICIAL"},
		{"name":"two","url":"https://dup.example","type":"OFFICIAL"},
		{"name":"three","url":"https://owner.example","type":"OFFICIAL","ownerId":"` + other.ID + `"}
	]}`
	response := doJSON(t, server, http.MethodPost, "/api/v1/assets/import", cookie, csrf, body)
	if response.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", response.Code, response.Body.String())
	}
	var result importResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 {
		t.Fatalf("expected only one asset created, got %+v", result)
	}
	if result.Results[1].Code != "ASSET_EXISTS" {
		t.Fatalf("duplicate row code=%q", result.Results[1].Code)
	}
	if result.Results[2].Code != "INVALID_OWNER" {
		t.Fatalf("cross-tenant owner row code=%q", result.Results[2].Code)
	}
}

func TestImportAssetsDryRunWritesNothing(t *testing.T) {
	repository, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	cookie, csrf := loginForTest(t, server, admin.Username)

	response := doJSON(t, server, http.MethodPost, "/api/v1/assets/import", cookie, csrf,
		`{"dryRun":true,"assets":[{"name":"preview","url":"https://preview.example","type":"OFFICIAL"}]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("dry run status=%d body=%s", response.Code, response.Body.String())
	}
	var result importResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 || result.Results[0].Status != "VALIDATED" {
		t.Fatalf("unexpected dry run result: %+v", result)
	}
	assets, _, err := repository.ListAssets(defaultTenant, store.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 0 {
		t.Fatalf("dry run created %d assets", len(assets))
	}
}

func TestImportAndExportRequireAdmin(t *testing.T) {
	repository, server, _ := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	operator := retentionUser(t, repository, "operator", domain.RoleOperator)
	cookie, csrf := loginForTest(t, server, operator.Username)

	if response := doJSON(t, server, http.MethodPost, "/api/v1/assets/import", cookie, csrf,
		`{"assets":[{"name":"x","url":"https://x.example","type":"OFFICIAL"}]}`); response.Code != http.StatusForbidden {
		t.Fatalf("operator import status=%d", response.Code)
	}
	if response := doJSON(t, server, http.MethodGet, "/api/v1/assets/export", cookie, csrf, ""); response.Code != http.StatusForbidden {
		t.Fatalf("operator export status=%d", response.Code)
	}
}

func TestExportAssetsJSONAndCSV(t *testing.T) {
	_, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	cookie, csrf := loginForTest(t, server, admin.Username)

	if response := doJSON(t, server, http.MethodPost, "/api/v1/assets", cookie, csrf,
		`{"name":"exported","url":"https://export.example","type":"OFFICIAL"}`); response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}

	jsonResponse := doJSON(t, server, http.MethodGet, "/api/v1/assets/export?format=json", cookie, csrf, "")
	if jsonResponse.Code != http.StatusOK || !bytes.Contains(jsonResponse.Body.Bytes(), []byte("https://export.example")) {
		t.Fatalf("json export status=%d body=%s", jsonResponse.Code, jsonResponse.Body.String())
	}

	csvResponse := doJSON(t, server, http.MethodGet, "/api/v1/assets/export?format=csv", cookie, csrf, "")
	if csvResponse.Code != http.StatusOK {
		t.Fatalf("csv export status=%d body=%s", csvResponse.Code, csvResponse.Body.String())
	}
	if contentType := csvResponse.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/csv") {
		t.Fatalf("csv content type=%q", contentType)
	}
	if !strings.Contains(csvResponse.Body.String(), "verificationMethod") || !strings.Contains(csvResponse.Body.String(), "exported") {
		t.Fatalf("unexpected csv body: %s", csvResponse.Body.String())
	}

	if bad := doJSON(t, server, http.MethodGet, "/api/v1/assets/export?format=xml", cookie, csrf, ""); bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid format status=%d", bad.Code)
	}
}

// retentionUserInTenant creates a user that exists only in the given tenant, so
// cross-tenant scoping checks have something real to reject.
func retentionUserInTenant(t *testing.T, repository *store.MemoryRepository, tenantID, username string, role domain.Role) domain.User {
	t.Helper()
	if _, err := repository.GetTenant(tenantID); err != nil {
		if err := repository.CreateTenant(domain.Tenant{ID: tenantID, Slug: "t" + username, Name: username}); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := security.HashPassword("a-long-password-for-testing")
	if err != nil {
		t.Fatal(err)
	}
	user := domain.User{
		ID: identity.New(), TenantID: tenantID, Username: username,
		PasswordHash: hash, Role: role, CreatedAt: time.Now().UTC(),
	}
	if err := repository.CreateUser(user); err != nil {
		t.Fatal(err)
	}
	return user
}
