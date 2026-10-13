package api

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/store"
)

func tenantBootstrapServer(t *testing.T) (*store.MemoryRepository, *Server, domain.User) {
	t.Helper()
	repository := store.NewMemoryRepository()
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	root := retentionUser(t, repository, "root", domain.RoleSuperAdmin)
	return repository, server, root
}

func tenantExists(t *testing.T, repository *store.MemoryRepository, slug string) bool {
	t.Helper()
	_, err := repository.GetTenantBySlug(context.Background(), slug)
	if err == store.ErrNotFound {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

// A tenant whose bootstrap administrator is refused must not exist at all.
//
// It used to: createTenant screened the password at 8 characters while
// HashPassword holds 12, so an 8-11 character password passed the handler, the
// tenant row was written, and only then did the hash fail -- a 500 with a tenant
// that has no administrator. Nothing in the API can repair that: createUser
// always writes into the caller's own tenant and only accepts OPERATOR or
// AUDITOR, so the tenant is unreachable and a retry hits TENANT_EXISTS.
func TestCreateTenantWithTooShortAdminPasswordLeavesNoTenant(t *testing.T) {
	repository, server, root := tenantBootstrapServer(t)
	cookie, csrf := loginForTest(t, server, root.Username)

	// 8 and 11 both passed the old screen; 11 is the last length the defect covered.
	for _, password := range []string{"short-8x", "eleven-char"} {
		if len(password) < 8 || len(password) >= 12 {
			t.Fatalf("test password %q is not inside the 8-11 window", password)
		}
		body := `{"name":"Victim","slug":"victim","adminUsername":"victim-admin","adminPassword":"` + password + `"}`
		response := doJSON(t, server, http.MethodPost, "/api/v1/tenants", cookie, csrf, body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("password %q status=%d body=%s", password, response.Code, response.Body.String())
		}
		if !bytes.Contains(response.Body.Bytes(), []byte("INVALID_PASSWORD")) {
			t.Fatalf("password %q body=%s", password, response.Body.String())
		}
		if tenantExists(t, repository, "victim") {
			t.Fatalf("password %q was refused but the tenant was still created", password)
		}
	}

	// The refusal must not have consumed the slug either: the corrected retry works.
	ok := doJSON(t, server, http.MethodPost, "/api/v1/tenants", cookie, csrf,
		`{"name":"Victim","slug":"victim","adminUsername":"victim-admin","adminPassword":"a-long-password-for-testing"}`)
	if ok.Code != http.StatusCreated {
		t.Fatalf("retry status=%d body=%s", ok.Code, ok.Body.String())
	}
	if !tenantExists(t, repository, "victim") {
		t.Fatal("tenant missing after a valid bootstrap")
	}
}

// The bootstrap administrator is the only way an ADMIN ever enters a tenant, so
// the happy path has to produce an account that can actually log in under the
// tenant slug.
func TestCreateTenantBootstrapAdminCanLogIn(t *testing.T) {
	_, server, root := tenantBootstrapServer(t)
	cookie, csrf := loginForTest(t, server, root.Username)

	response := doJSON(t, server, http.MethodPost, "/api/v1/tenants", cookie, csrf,
		`{"name":"Acme","slug":"acme","adminUsername":"acme-admin","adminPassword":"a-long-password-for-testing"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	login := doJSON(t, server, http.MethodPost, "/api/v1/auth/session", nil, "",
		`{"tenant":"acme","username":"acme-admin","password":"a-long-password-for-testing"}`)
	if login.Code != http.StatusCreated {
		t.Fatalf("bootstrap admin cannot log in: status=%d body=%s", login.Code, login.Body.String())
	}
}
