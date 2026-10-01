package api

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestRetentionMaintenanceRequiresAdminAndSupportsDryRun(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	operator := retentionUser(t, repository, "operator", domain.RoleOperator)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	expired, err := evidenceStore.Put(defaultTenant, "asset", "job", "page.html", "text/html", []byte("expired"))
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Hour)
	expired.RetainUntil = &past
	if err := repository.CreateEvidence(context.Background(), expired); err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)

	operatorCookie, operatorCSRF := loginForTest(t, server, operator.Username)
	forbidden := authorizedRequest(t, server, operatorCookie, operatorCSRF, `{"execute":false}`)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("operator retention status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}

	adminCookie, adminCSRF := loginForTest(t, server, admin.Username)
	dryRun := authorizedRequest(t, server, adminCookie, adminCSRF, `{"execute":false}`)
	if dryRun.Code != http.StatusOK || !bytes.Contains(dryRun.Body.Bytes(), []byte(`"expiredEvidence":1`)) {
		t.Fatalf("dry run status=%d body=%s", dryRun.Code, dryRun.Body.String())
	}
	if _, err := repository.GetEvidence(context.Background(), defaultTenant, expired.ID); err != nil {
		t.Fatalf("dry run deleted evidence: %v", err)
	}

	execute := authorizedRequest(t, server, adminCookie, adminCSRF, `{"execute":true}`)
	if execute.Code != http.StatusOK || !bytes.Contains(execute.Body.Bytes(), []byte(`"deletedEvidence":1`)) {
		t.Fatalf("execute status=%d body=%s", execute.Code, execute.Body.String())
	}
	if _, err := repository.GetEvidence(context.Background(), defaultTenant, expired.ID); err != store.ErrNotFound {
		t.Fatalf("executed retention left metadata: %v", err)
	}
}

func retentionUser(t *testing.T, repository *store.MemoryRepository, username string, role domain.Role) domain.User {
	t.Helper()
	hash, err := security.HashPassword("a-long-password-for-testing")
	if err != nil {
		t.Fatal(err)
	}
	user := domain.User{
		ID: identity.New(), TenantID: defaultTenant, Username: username,
		PasswordHash: hash, Role: role, CreatedAt: time.Now().UTC(),
	}
	if err := repository.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	return user
}

func retentionServer(repository store.Repository, evidenceStore evidence.Store) *Server {
	guard := security.NewURLGuard(false)
	broker := events.NewBroker()
	service := detection.NewService(
		repository, evidenceStore, detection.NewChecker(guard), nil,
		notify.NewDispatcher(repository, guard), broker,
		slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)),
	)
	server := NewServer(
		repository, auth.NewService(repository, time.Hour), service, evidenceStore, guard, broker,
		slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)), false,
	)
	server.SetRetentionPolicy(1, 1)
	return server
}

func loginForTest(t *testing.T, server *Server, username string) (*http.Cookie, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session",
		bytes.NewBufferString(`{"username":"`+username+`","password":"a-long-password-for-testing"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := decodeResponse(response, &body); err != nil {
		t.Fatal(err)
	}
	return response.Result().Cookies()[0], body.CSRFToken
}

func authorizedRequest(t *testing.T, server *Server, cookie *http.Cookie, csrf, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/maintenance/retention", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func decodeResponse(response *httptest.ResponseRecorder, target any) error {
	return json.NewDecoder(response.Body).Decode(target)
}
