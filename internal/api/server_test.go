package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"webmonitoring/internal/auth"
	"webmonitoring/internal/detection"
	"webmonitoring/internal/domain"
	"webmonitoring/internal/events"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/notify"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

func TestSessionAndCSRFGates(t *testing.T) {
	repository := store.NewMemoryRepository()
	hash, err := security.HashPassword("a-long-password-for-testing")
	if err != nil {
		t.Fatal(err)
	}
	user := domain.User{
		ID: identity.New(), TenantID: defaultTenant, Username: "admin",
		PasswordHash: hash, Role: domain.RoleAdmin, CreatedAt: time.Now().UTC(),
	}
	if err := repository.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	guard := security.NewURLGuard(false)
	broker := events.NewBroker()
	checker := detection.NewChecker(guard)
	service := detection.NewService(
		repository, evidenceStore, checker, nil, notify.NewDispatcher(repository, guard), broker,
		slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)),
	)
	server := NewServer(repository, auth.NewService(repository, time.Hour), service, evidenceStore, guard, broker,
		slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)), false)

	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", bytes.NewBufferString(`{"username":"admin","password":"a-long-password-for-testing"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	server.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusCreated {
		t.Fatalf("login status: %d: %s", loginResponse.Code, loginResponse.Body.String())
	}
	cookie := loginResponse.Result().Cookies()[0]

	unauthorized := httptest.NewRequest(http.MethodPost, "/api/v1/assets", bytes.NewBufferString(`{}`))
	unauthorizedResponse := httptest.NewRecorder()
	server.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected auth gate, got %d", unauthorizedResponse.Code)
	}

	withoutCSRF := httptest.NewRequest(http.MethodPost, "/api/v1/assets", bytes.NewBufferString(`{"name":"x","url":"https://example.com","type":"OFFICIAL"}`))
	withoutCSRF.AddCookie(cookie)
	withoutCSRF.Header.Set("Content-Type", "application/json")
	withoutCSRFResponse := httptest.NewRecorder()
	server.ServeHTTP(withoutCSRFResponse, withoutCSRF)
	if withoutCSRFResponse.Code != http.StatusForbidden {
		t.Fatalf("expected CSRF gate, got %d", withoutCSRFResponse.Code)
	}
}

func TestRedactEndpoint(t *testing.T) {
	got := redactEndpoint("https://user:pass@example.com/hooks/private-token?key=secret#fragment")
	if got != "https://example.com/..." {
		t.Fatalf("unexpected redacted endpoint: %q", got)
	}
	if got := redactEndpoint("not-a-url"); got != "[redacted]" {
		t.Fatalf("invalid endpoint was not redacted: %q", got)
	}
}
