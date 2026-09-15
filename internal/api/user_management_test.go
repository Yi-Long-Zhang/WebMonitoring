package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/store"
)

func userMgmtServer(t *testing.T) (*store.MemoryRepository, *Server) {
	t.Helper()
	repository := store.NewMemoryRepository()
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return repository, retentionServer(repository, evidenceStore)
}

func doJSON(t *testing.T, server *Server, method, path string, cookie *http.Cookie, csrf, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func loginAs(t *testing.T, server *Server, username, password string) int {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session",
		bytes.NewBufferString(`{"username":"`+username+`","password":"`+password+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response.Code
}

func TestUserCreateListDisableAndPasswordReset(t *testing.T) {
	repository, server := userMgmtServer(t)
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	cookie, csrf := loginForTest(t, server, admin.Username)

	create := doJSON(t, server, http.MethodPost, "/api/v1/users", cookie, csrf,
		`{"username":"op","password":"a-long-password-for-testing","role":"OPERATOR"}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("create user status=%d body=%s", create.Code, create.Body.String())
	}
	var created domain.User
	if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Role != domain.RoleOperator || created.Disabled {
		t.Fatalf("unexpected created user: %#v", created)
	}

	list := doJSON(t, server, http.MethodGet, "/api/v1/users", cookie, "", "")
	if list.Code != http.StatusOK || !bytes.Contains(list.Body.Bytes(), []byte(`"op"`)) {
		t.Fatalf("list users status=%d body=%s", list.Code, list.Body.String())
	}

	disable := doJSON(t, server, http.MethodPatch, "/api/v1/users/"+created.ID, cookie, csrf, `{"disabled":true}`)
	if disable.Code != http.StatusOK {
		t.Fatalf("disable user status=%d body=%s", disable.Code, disable.Body.String())
	}
	if loginAs(t, server, "op", "a-long-password-for-testing") != http.StatusUnauthorized {
		t.Fatalf("disabled user was able to log in")
	}

	enable := doJSON(t, server, http.MethodPatch, "/api/v1/users/"+created.ID, cookie, csrf, `{"disabled":false}`)
	if enable.Code != http.StatusOK {
		t.Fatalf("enable user status=%d body=%s", enable.Code, enable.Body.String())
	}

	reset := doJSON(t, server, http.MethodPost, "/api/v1/users/"+created.ID+"/password", cookie, csrf,
		`{"password":"another-long-password-for-testing"}`)
	if reset.Code != http.StatusNoContent {
		t.Fatalf("reset password status=%d body=%s", reset.Code, reset.Body.String())
	}
	if loginAs(t, server, "op", "another-long-password-for-testing") != http.StatusCreated {
		t.Fatalf("login with reset password failed")
	}
}

func TestUserManagementRoleGuards(t *testing.T) {
	repository, server := userMgmtServer(t)
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	auditor := retentionUser(t, repository, "auditor", domain.RoleAuditor)

	auditorCookie, auditorCSRF := loginForTest(t, server, auditor.Username)
	forbidden := doJSON(t, server, http.MethodPost, "/api/v1/users", auditorCookie, auditorCSRF,
		`{"username":"x","password":"a-long-password-for-testing","role":"OPERATOR"}`)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("auditor create user status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}

	adminCookie, adminCSRF := loginForTest(t, server, admin.Username)
	for _, role := range []string{"ADMIN", "SUPER_ADMIN"} {
		resp := doJSON(t, server, http.MethodPost, "/api/v1/users", adminCookie, adminCSRF,
			`{"username":"bad-`+role+`","password":"a-long-password-for-testing","role":"`+role+`"}`)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("create %s status=%d body=%s", role, resp.Code, resp.Body.String())
		}
	}

	short := doJSON(t, server, http.MethodPost, "/api/v1/users", adminCookie, adminCSRF,
		`{"username":"shorty","password":"short","role":"OPERATOR"}`)
	if short.Code != http.StatusBadRequest {
		t.Fatalf("short password status=%d body=%s", short.Code, short.Body.String())
	}
}

func TestAdminCannotDisableOrDemoteSelf(t *testing.T) {
	repository, server := userMgmtServer(t)
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	cookie, csrf := loginForTest(t, server, admin.Username)

	for _, body := range []string{`{"disabled":true}`, `{"role":"OPERATOR"}`} {
		resp := doJSON(t, server, http.MethodPatch, "/api/v1/users/"+admin.ID, cookie, csrf, body)
		if resp.Code != http.StatusConflict {
			t.Fatalf("self mutation status=%d body=%s", resp.Code, resp.Body.String())
		}
	}
}

func TestLastEnabledAdminCannotBeRemoved(t *testing.T) {
	repository, server := userMgmtServer(t)
	root := retentionUser(t, repository, "root", domain.RoleSuperAdmin)
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	cookie, csrf := loginForTest(t, server, root.Username)

	resp := doJSON(t, server, http.MethodPatch, "/api/v1/users/"+admin.ID, cookie, csrf, `{"disabled":true}`)
	if resp.Code != http.StatusConflict {
		t.Fatalf("last admin disable status=%d body=%s", resp.Code, resp.Body.String())
	}
	if !bytes.Contains(resp.Body.Bytes(), []byte(`LAST_ADMIN`)) {
		t.Fatalf("expected LAST_ADMIN, body=%s", resp.Body.String())
	}

	// The bootstrap SUPER_ADMIN itself is not tenant-managed.
	rootResp := doJSON(t, server, http.MethodPatch, "/api/v1/users/"+root.ID, cookie, csrf, `{"disabled":true}`)
	if rootResp.Code != http.StatusForbidden {
		t.Fatalf("super admin patch status=%d body=%s", rootResp.Code, rootResp.Body.String())
	}
}

func TestChangeOwnPassword(t *testing.T) {
	repository, server := userMgmtServer(t)
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	cookie, csrf := loginForTest(t, server, admin.Username)

	bad := doJSON(t, server, http.MethodPost, "/api/v1/auth/password", cookie, csrf,
		`{"currentPassword":"wrong-password-here","newPassword":"a-new-long-password-123"}`)
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("wrong current password status=%d body=%s", bad.Code, bad.Body.String())
	}

	good := doJSON(t, server, http.MethodPost, "/api/v1/auth/password", cookie, csrf,
		`{"currentPassword":"a-long-password-for-testing","newPassword":"a-new-long-password-123"}`)
	if good.Code != http.StatusNoContent {
		t.Fatalf("change password status=%d body=%s", good.Code, good.Body.String())
	}

	if loginAs(t, server, "admin", "a-new-long-password-123") != http.StatusCreated {
		t.Fatalf("login with new password failed")
	}
	if loginAs(t, server, "admin", "a-long-password-for-testing") != http.StatusUnauthorized {
		t.Fatalf("login with old password unexpectedly succeeded")
	}
}
