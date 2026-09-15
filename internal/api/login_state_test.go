package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

func loginStateCipher(t *testing.T) *security.SecretCipher {
	t.Helper()
	cipher, err := security.NewSecretCipher(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func loginStateFixture(t *testing.T) (*store.MemoryRepository, *Server, *http.Cookie, string) {
	t.Helper()
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	now := time.Now().UTC()
	asset := domain.Asset{
		ID: "login-asset", TenantID: defaultTenant, Name: "login",
		URL: "https://example.com/", Type: domain.AssetLogin, OwnerID: admin.ID,
		Enabled: true, VerificationStatus: domain.VerificationVerified, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateAsset(asset); err != nil {
		t.Fatal(err)
	}
	if err := repository.UpsertPolicy(domain.MonitorPolicy{
		ID: "login-policy", TenantID: defaultTenant, AssetID: asset.ID, Version: 1,
		HTTPIntervalSeconds: 300, BrowserIntervalSeconds: 1800, TimeoutSeconds: 30,
		MaxResponseBytes: 5 * 1024 * 1024, IgnoreSelectors: []string{}, TrustedDomains: []string{},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	server.SetSecretCipher(loginStateCipher(t))
	cookie, csrf := loginForTest(t, server, "admin")
	return repository, server, cookie, csrf
}

// TestUpdatePolicyEncryptsAndRedactsLoginCredentials verifies that plaintext
// credentials are encrypted before storage, ciphertext is stripped from the API
// response, and re-saving without a password preserves the stored credentials.
func TestUpdatePolicyEncryptsAndRedactsLoginCredentials(t *testing.T) {
	repository, server, cookie, csrf := loginStateFixture(t)

	body := `{
		"httpIntervalSeconds": 300,
		"browserIntervalSeconds": 1800,
		"timeoutSeconds": 30,
		"maxResponseBytes": 5242880,
		"ignoreSelectors": [],
		"trustedDomains": [],
		"auth": {
			"loginUrl": "https://8.8.8.8/login",
			"usernameField": "email",
			"passwordField": "password",
			"username": "user@example.com",
			"password": "s3cret"
		}
	}`
	response := doJSON(t, server, http.MethodPut, "/api/v1/policies/login-asset", cookie, csrf, body)
	if response.Code != http.StatusOK {
		t.Fatalf("update policy status=%d body=%s", response.Code, response.Body.String())
	}

	stored, err := repository.GetPolicy(defaultTenant, "login-asset")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Auth == nil {
		t.Fatal("expected auth config to be stored")
	}
	if stored.Auth.EncryptedUsername == "" || stored.Auth.EncryptedPassword == "" {
		t.Fatal("expected encrypted credentials to be stored")
	}
	if stored.Auth.EncryptedUsername == "user@example.com" || stored.Auth.EncryptedPassword == "s3cret" {
		t.Fatal("credentials stored in plaintext")
	}
	if stored.Auth.LoginURL != "https://8.8.8.8/login" {
		t.Fatalf("unexpected login URL: %q", stored.Auth.LoginURL)
	}

	var payload struct {
		Auth struct {
			CredentialsSet    bool   `json:"credentialsSet"`
			EncryptedUsername string `json:"encryptedUsername"`
			EncryptedPassword string `json:"encryptedPassword"`
		} `json:"auth"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Auth.CredentialsSet {
		t.Fatal("expected credentialsSet=true in redacted response")
	}
	if payload.Auth.EncryptedUsername != "" || payload.Auth.EncryptedPassword != "" {
		t.Fatal("response leaked encrypted credentials")
	}

	// Re-saving without a password preserves the previously stored credentials.
	reupdate := doJSON(t, server, http.MethodPut, "/api/v1/policies/login-asset", cookie, csrf,
		`{"httpIntervalSeconds":600,"browserIntervalSeconds":1800,"timeoutSeconds":30,"maxResponseBytes":5242880,"ignoreSelectors":[],"trustedDomains":[],"auth":{"loginUrl":"https://8.8.8.8/login","usernameField":"email","passwordField":"password"}}`)
	if reupdate.Code != http.StatusOK {
		t.Fatalf("re-update status=%d body=%s", reupdate.Code, reupdate.Body.String())
	}
	preserved, err := repository.GetPolicy(defaultTenant, "login-asset")
	if err != nil {
		t.Fatal(err)
	}
	if preserved.Auth == nil || preserved.Auth.EncryptedUsername == "" {
		t.Fatal("credentials were not preserved on re-save without password")
	}
}

// TestUpdatePolicyRejectsPrivateLoginURL ensures a login endpoint inside the
// private network is refused before it is ever stored.
func TestUpdatePolicyRejectsPrivateLoginURL(t *testing.T) {
	_, server, cookie, csrf := loginStateFixture(t)

	body := `{
		"httpIntervalSeconds": 300,
		"browserIntervalSeconds": 1800,
		"timeoutSeconds": 30,
		"maxResponseBytes": 5242880,
		"ignoreSelectors": [],
		"trustedDomains": [],
		"auth": {
			"loginUrl": "http://127.0.0.1/login",
			"usernameField": "email",
			"passwordField": "password",
			"username": "user@example.com",
			"password": "s3cret"
		}
	}`
	response := doJSON(t, server, http.MethodPut, "/api/v1/policies/login-asset", cookie, csrf, body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected private login URL rejection, got %d: %s", response.Code, response.Body.String())
	}
}
