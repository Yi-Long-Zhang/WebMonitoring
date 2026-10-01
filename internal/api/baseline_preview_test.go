package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/store"
)

// baselinePreviewServer seeds one baseline and returns a server plus the login
// cookie for the default tenant's admin.
func baselinePreviewServer(t *testing.T, baseline domain.BaselineVersion) (*store.MemoryRepository, *Server, *http.Cookie) {
	t.Helper()
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	if err := repository.CreateBaseline(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	cookie, _ := loginForTest(t, server, admin.Username)
	return repository, server, cookie
}

func seededBaseline(id, tenantID string) domain.BaselineVersion {
	now := time.Now().UTC()
	return domain.BaselineVersion{
		ID: id, TenantID: tenantID, AssetID: "asset-" + id, Version: 1,
		Status: domain.BaselineCandidate,
		Snapshot: domain.Snapshot{
			URL: "https://example.com/", FinalURL: "https://example.com/",
			StatusCode: 200, Title: "Example Domain",
			ContentHash: "content-hash", TextHash: "text-hash",
			ResolvedIPs: []string{"203.0.113.10"}, DurationMillis: 42, CapturedAt: now,
		},
		EvidenceIDs: []string{"evidence-" + id},
		CreatedBy:   "creator-" + id,
		CreatedAt:   now,
	}
}

// The snapshot was already on the wire via GET /baselines; this asserts the new
// detail endpoint returns the same thing for the owning tenant, so the console
// has a declared contract to read it from.
func TestBaselinePreviewReturnsSnapshot(t *testing.T) {
	baseline := seededBaseline("baseline-1", defaultTenant)
	_, server, cookie := baselinePreviewServer(t, baseline)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/baselines/"+baseline.ID, nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("baseline preview status=%d body=%s", response.Code, response.Body.String())
	}
	var body domain.BaselineVersion
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Snapshot.Title != "Example Domain" {
		t.Fatalf("snapshot title=%q body=%s", body.Snapshot.Title, response.Body.String())
	}
	if body.Snapshot.ContentHash != "content-hash" {
		t.Fatalf("snapshot contentHash=%q", body.Snapshot.ContentHash)
	}
	if len(body.EvidenceIDs) != 1 || body.EvidenceIDs[0] != "evidence-baseline-1" {
		t.Fatalf("evidenceIds=%v", body.EvidenceIDs)
	}
}

// A baseline owned by another tenant must be indistinguishable from a missing
// one: 404, never 403, so the response does not confirm the id exists.
func TestBaselinePreviewHidesOtherTenants(t *testing.T) {
	foreign := seededBaseline("baseline-foreign", "other-tenant")
	_, server, cookie := baselinePreviewServer(t, foreign)

	for _, id := range []string{foreign.ID, "does-not-exist"} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/baselines/"+id, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("baseline %q status=%d body=%s", id, response.Code, response.Body.String())
		}
		if body := response.Body.String(); strings.Contains(body, "Example Domain") {
			t.Fatalf("baseline %q leaked snapshot: %s", id, body)
		}
	}
}

// An unauthenticated read must not reach the detail endpoint at all; the route
// sits behind the same session gate as every other protected read.
func TestBaselinePreviewRequiresSession(t *testing.T) {
	baseline := seededBaseline("baseline-1", defaultTenant)
	_, server, _ := baselinePreviewServer(t, baseline)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/baselines/"+baseline.ID, nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated baseline preview status=%d body=%s", response.Code, response.Body.String())
	}
}
