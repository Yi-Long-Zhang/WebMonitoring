package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

func signedURLKey() string {
	return base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
}

func newSignedSigner(t *testing.T) *security.SignedURLSigner {
	t.Helper()
	signer, err := security.NewSignedURLSigner(signedURLKey())
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func seedEvidence(t *testing.T, repository store.Repository, evidenceStore evidence.Store, content string) domain.Evidence {
	t.Helper()
	item, err := evidenceStore.Put(defaultTenant, "asset", "job", "page.html", "text/html", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateEvidence(item); err != nil {
		t.Fatal(err)
	}
	return item
}

func TestEvidenceSignedURLLocalFlow(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item := seedEvidence(t, repository, evidenceStore, "signed-content")

	server := retentionServer(repository, evidenceStore)
	server.SetSignedURLs(newSignedSigner(t), 300)

	cookie, csrf := loginForTest(t, server, admin.Username)
	issue := doJSON(t, server, http.MethodPost, "/api/v1/evidence/"+item.ID+"/signed-url", cookie, csrf, "")
	if issue.Code != http.StatusOK {
		t.Fatalf("issue status=%d body=%s", issue.Code, issue.Body.String())
	}
	var signed struct {
		URL       string `json:"url"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := decodeResponse(issue, &signed); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(signed.URL, "/api/v1/evidence/signed/") {
		t.Fatalf("unexpected signed url %q", signed.URL)
	}
	if _, err := time.Parse(time.RFC3339, signed.ExpiresAt); err != nil {
		t.Fatalf("bad expiresAt %q: %v", signed.ExpiresAt, err)
	}

	// The signed URL is served without a session; the token is the authorization.
	get := httptest.NewRecorder()
	server.ServeHTTP(get, httptest.NewRequest(http.MethodGet, signed.URL, nil))
	if get.Code != http.StatusOK || get.Body.String() != "signed-content" {
		t.Fatalf("signed get status=%d body=%q", get.Code, get.Body.String())
	}
	if get.Header().Get("X-Content-Type-Options") != "nosniff" || get.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing security headers: %v", get.Header())
	}

	// A tampered token is rejected. The mutation must land in the middle of the
	// signature: the final base64url character carries only unused low bits, so
	// corrupting it can decode to the very same signature bytes and would make
	// this assertion pass or fail depending on the digest.
	dot := strings.LastIndex(signed.URL, ".")
	signature := []byte(signed.URL[dot+1:])
	if len(signature) < 8 {
		t.Fatalf("unexpectedly short signature in %q", signed.URL)
	}
	middle := len(signature) / 2
	if signature[middle] == 'A' {
		signature[middle] = 'B'
	} else {
		signature[middle] = 'A'
	}
	tampered := httptest.NewRecorder()
	server.ServeHTTP(tampered, httptest.NewRequest(
		http.MethodGet, signed.URL[:dot+1]+string(signature), nil,
	))
	if tampered.Code != http.StatusNotFound {
		t.Fatalf("tampered token status=%d", tampered.Code)
	}

	// An expired token is rejected.
	expiredToken := newSignedSigner(t).Encode(defaultTenant + "|" + item.ID + "|1")
	expired := httptest.NewRecorder()
	server.ServeHTTP(expired, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/signed/"+expiredToken, nil))
	if expired.Code != http.StatusNotFound {
		t.Fatalf("expired token status=%d", expired.Code)
	}

	// A token for the wrong tenant does not resolve to the evidence.
	wrongTenantToken := newSignedSigner(t).Encode("00000000-0000-4000-8000-000000000099|" + item.ID + "|4102444800")
	wrongTenant := httptest.NewRecorder()
	server.ServeHTTP(wrongTenant, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/signed/"+wrongTenantToken, nil))
	if wrongTenant.Code != http.StatusNotFound {
		t.Fatalf("wrong-tenant token status=%d", wrongTenant.Code)
	}
}

func TestEvidenceSignedURLRequiresRole(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	auditor := retentionUser(t, repository, "auditor", domain.RoleAuditor)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item := seedEvidence(t, repository, evidenceStore, "x")

	server := retentionServer(repository, evidenceStore)
	server.SetSignedURLs(newSignedSigner(t), 300)

	auditorCookie, auditorCSRF := loginForTest(t, server, auditor.Username)
	forbidden := doJSON(t, server, http.MethodPost, "/api/v1/evidence/"+item.ID+"/signed-url", auditorCookie, auditorCSRF, "")
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("auditor status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}

	adminCookie, adminCSRF := loginForTest(t, server, admin.Username)
	allowed := doJSON(t, server, http.MethodPost, "/api/v1/evidence/"+item.ID+"/signed-url", adminCookie, adminCSRF, "")
	if allowed.Code != http.StatusOK {
		t.Fatalf("admin status=%d body=%s", allowed.Code, allowed.Body.String())
	}
}

// presigningStore decorates a local store with the optional URLPresigner
// interface so the S3 dispatch branch is exercised without a real S3 backend.
type presigningStore struct {
	evidence.Store
}

func (p *presigningStore) PresignedGetURL(_ context.Context, objectKey string, ttl time.Duration) (string, error) {
	return "https://s3.example.test/" + objectKey + "?ttl=" + ttl.String(), nil
}

func TestEvidenceSignedURLS3Presign(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	local, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item := seedEvidence(t, repository, local, "x")

	server := retentionServer(repository, &presigningStore{Store: local})

	cookie, csrf := loginForTest(t, server, admin.Username)
	issue := doJSON(t, server, http.MethodPost, "/api/v1/evidence/"+item.ID+"/signed-url", cookie, csrf, "")
	if issue.Code != http.StatusOK {
		t.Fatalf("issue status=%d body=%s", issue.Code, issue.Body.String())
	}
	var signed struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(issue.Body).Decode(&signed); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(signed.URL, "https://s3.example.test/") || !strings.Contains(signed.URL, item.ObjectKey) {
		t.Fatalf("unexpected presigned url %q", signed.URL)
	}
}
