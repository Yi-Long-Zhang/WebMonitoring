package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"webmonitoring/internal/auth"
	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/store"
)

// A Redis outage must not read as "session expired": an authenticated request
// has to fail with 5xx so the client retries instead of dropping the session.
func TestAuthenticatedRequestReturns500WhenSessionStoreFails(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)

	redisServer := miniredis.RunT(t)
	server.auth.SetSessionStore(auth.NewRedisSessionStore(redisServer.Addr()))
	// The session record now lives in the (about to fail) Redis store.
	cookie, _ := loginForTest(t, server, admin.Username)
	redisServer.Close()

	response := doJSON(t, server, http.MethodGet, "/api/v1/dashboard", cookie, "", "")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (backend failure, not 401 logout); body=%s",
			response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "SESSION_EXPIRED") {
		t.Fatalf("a store outage must not be reported as SESSION_EXPIRED: %s", response.Body.String())
	}
}

// Login with correct credentials must fail with 500 when the session cannot
// be persisted — not 401 (the credentials were fine) and not 201 (the client
// would hold a cookie the next request rejects).
func TestLoginReturns500WhenSessionStoreFails(t *testing.T) {
	repository := store.NewMemoryRepository()
	retentionUser(t, repository, "admin", domain.RoleAdmin)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)

	redisServer := miniredis.RunT(t)
	server.auth.SetSessionStore(auth.NewRedisSessionStore(redisServer.Addr()))
	redisServer.Close()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session",
		bytes.NewBufferString(`{"username":"admin","password":"a-long-password-for-testing"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "SESSION_STORE_UNAVAILABLE") {
		t.Fatalf("expected SESSION_STORE_UNAVAILABLE, got %s", response.Body.String())
	}
}
