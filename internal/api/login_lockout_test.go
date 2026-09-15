package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"webmonitoring/internal/auth"
	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/store"
)

func TestLoginLocksAccountAfterRepeatedFailures(t *testing.T) {
	repository := store.NewMemoryRepository()
	retentionUser(t, repository, "admin", domain.RoleAdmin)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	server.SetLoginGuard(auth.NewMemoryLoginGuard(3, time.Minute))

	for i := 0; i < 3; i++ {
		if rec := doLogin(t, server, "admin", "wrong-password"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failed login status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	rec := doLogin(t, server, "admin", "wrong-password")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("locked login status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "ACCOUNT_LOCKED" {
		t.Fatalf("expected ACCOUNT_LOCKED, got %q", body.Code)
	}
}

func TestLoginSuccessResetsLockoutCounter(t *testing.T) {
	repository := store.NewMemoryRepository()
	retentionUser(t, repository, "admin", domain.RoleAdmin)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	server.SetLoginGuard(auth.NewMemoryLoginGuard(3, time.Minute))

	for i := 0; i < 2; i++ {
		if rec := doLogin(t, server, "admin", "wrong-password"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failed login status=%d", rec.Code)
		}
	}
	if rec := doLogin(t, server, "admin", "a-long-password-for-testing"); rec.Code != http.StatusCreated {
		t.Fatalf("successful login status=%d body=%s", rec.Code, rec.Body.String())
	}
	// If the success had NOT reset the counter, the second of these would trip
	// the lock (counter would reach 4). Assert each stays INVALID_CREDENTIALS.
	for i := 0; i < 2; i++ {
		rec := doLogin(t, server, "admin", "wrong-password")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("post-reset failed login status=%d body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Code != "INVALID_CREDENTIALS" {
			t.Fatalf("expected INVALID_CREDENTIALS after reset, got %q", body.Code)
		}
	}
}

func doLogin(t *testing.T, server *Server, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session",
		bytes.NewBufferString(`{"username":"`+username+`","password":"`+password+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}
