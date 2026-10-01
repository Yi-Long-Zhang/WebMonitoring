package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/store"
)

const tenantDeleteTarget = "00000000-0000-4000-8000-0000000000aa"

// tenantDeleteServer builds a server with a SUPER_ADMIN in the default tenant
// and a second, disposable tenant to delete.
func tenantDeleteServer(t *testing.T) (*store.MemoryRepository, *Server, domain.User) {
	t.Helper()
	repository := store.NewMemoryRepository()
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateTenant(context.Background(), domain.Tenant{ID: tenantDeleteTarget, Slug: "doomed", Name: "Doomed"}); err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	root := retentionUser(t, repository, "root", domain.RoleSuperAdmin)
	return repository, server, root
}

func TestDeleteTenantRequiresConfirmationAndFailsWhenNotEmpty(t *testing.T) {
	repository, server, root := tenantDeleteServer(t)
	cookie, csrf := loginForTest(t, server, root.Username)
	path := "/api/v1/tenants/" + tenantDeleteTarget

	// Wrong confirmation is refused.
	mismatch := doJSON(t, server, http.MethodDelete, path, cookie, csrf, `{"confirm":"wrong"}`)
	if mismatch.Code != http.StatusBadRequest || !bytes.Contains(mismatch.Body.Bytes(), []byte("CONFIRM_MISMATCH")) {
		t.Fatalf("mismatch status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}

	// Seed a user so the tenant is no longer empty.
	if err := repository.CreateUser(context.Background(), domain.User{
		ID: "aaaaaaaa-0000-4000-8000-000000000001", TenantID: tenantDeleteTarget,
		Username: "resident", PasswordHash: "x", Role: domain.RoleAdmin,
	}); err != nil {
		t.Fatal(err)
	}

	notEmpty := doJSON(t, server, http.MethodDelete, path, cookie, csrf, `{"confirm":"doomed"}`)
	if notEmpty.Code != http.StatusConflict || !bytes.Contains(notEmpty.Body.Bytes(), []byte("TENANT_NOT_EMPTY")) {
		t.Fatalf("not-empty status=%d body=%s", notEmpty.Code, notEmpty.Body.String())
	}
	if !bytes.Contains(notEmpty.Body.Bytes(), []byte(`"users":1`)) {
		t.Fatalf("expected resource counts in body=%s", notEmpty.Body.String())
	}
	if _, err := repository.GetTenant(context.Background(), tenantDeleteTarget); err != nil {
		t.Fatalf("refused delete removed the tenant: %v", err)
	}
}

func TestDeleteTenantCascadeRemovesTenantScopedData(t *testing.T) {
	repository, server, root := tenantDeleteServer(t)
	if err := repository.CreateUser(context.Background(), domain.User{
		ID: "aaaaaaaa-0000-4000-8000-000000000001", TenantID: tenantDeleteTarget,
		Username: "resident", PasswordHash: "x", Role: domain.RoleAdmin,
	}); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := loginForTest(t, server, root.Username)

	response := doJSON(t, server, http.MethodDelete, "/api/v1/tenants/"+tenantDeleteTarget, cookie, csrf,
		`{"confirm":"doomed","cascade":true}`)
	if response.Code != http.StatusOK {
		t.Fatalf("cascade status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		TenantID         string         `json:"tenantId"`
		DeletedResources map[string]int `json:"deletedResources"`
		OrphanedObjects  int            `json:"orphanedEvidenceObjects"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.TenantID != tenantDeleteTarget || payload.DeletedResources["users"] != 1 {
		t.Fatalf("unexpected result: %+v", payload)
	}
	if _, err := repository.GetTenant(context.Background(), tenantDeleteTarget); err != store.ErrNotFound {
		t.Fatalf("tenant still present: %v", err)
	}
	if _, err := repository.GetUser(context.Background(), tenantDeleteTarget, "aaaaaaaa-0000-4000-8000-000000000001"); err != store.ErrNotFound {
		t.Fatalf("tenant user still present: %v", err)
	}
}

func TestDeleteTenantRefusesSelfAndDefault(t *testing.T) {
	_, server, root := tenantDeleteServer(t)
	cookie, csrf := loginForTest(t, server, root.Username)

	self := doJSON(t, server, http.MethodDelete, "/api/v1/tenants/"+defaultTenant, cookie, csrf, `{"confirm":"default"}`)
	if self.Code != http.StatusConflict || !bytes.Contains(self.Body.Bytes(), []byte("TENANT_SELF_DELETE")) {
		t.Fatalf("self delete status=%d body=%s", self.Code, self.Body.String())
	}
}

func TestDisabledTenantCannotLogIn(t *testing.T) {
	repository, server, root := tenantDeleteServer(t)
	if err := repository.CreateUser(context.Background(), domain.User{
		ID: "aaaaaaaa-0000-4000-8000-000000000002", TenantID: tenantDeleteTarget,
		Username: "resident", PasswordHash: "x", Role: domain.RoleAdmin,
	}); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := loginForTest(t, server, root.Username)
	response := doJSON(t, server, http.MethodPatch, "/api/v1/tenants/"+tenantDeleteTarget, cookie, csrf,
		`{"disabled":true}`)
	if response.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", response.Code, response.Body.String())
	}

	// A resident of the disabled tenant cannot obtain a session.
	login := doJSON(t, server, http.MethodPost, "/api/v1/auth/session", nil, "",
		`{"tenant":"doomed","username":"resident","password":"a-long-password-for-testing"}`)
	if login.Code != http.StatusUnauthorized {
		t.Fatalf("disabled tenant login status=%d body=%s", login.Code, login.Body.String())
	}

	// The default tenant cannot be disabled: that would lock out the platform.
	protect := doJSON(t, server, http.MethodPatch, "/api/v1/tenants/"+defaultTenant, cookie, csrf, `{"disabled":true}`)
	if protect.Code != http.StatusConflict || !bytes.Contains(protect.Body.Bytes(), []byte("TENANT_PROTECTED")) {
		t.Fatalf("default tenant disable status=%d body=%s", protect.Code, protect.Body.String())
	}
}
