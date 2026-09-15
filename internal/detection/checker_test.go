package detection

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/security"
)

func checkerTestCipher(t *testing.T) *security.SecretCipher {
	t.Helper()
	cipher, err := security.NewSecretCipher(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

// TestCheckerLoginState verifies the HTTP checker authenticates against the
// login URL and snapshots the protected (authenticated) page rather than the
// login form.
func TestCheckerLoginState(t *testing.T) {
	cipher := checkerTestCipher(t)
	mux := http.NewServeMux()
	var loginCalls int
	mux.HandleFunc("/login", func(writer http.ResponseWriter, request *http.Request) {
		loginCalls++
		if err := request.ParseForm(); err != nil {
			t.Errorf("parse login form: %v", err)
		}
		if request.PostFormValue("email") != "user@example.com" || request.PostFormValue("password") != "s3cret" {
			http.Error(writer, "bad credentials", http.StatusUnauthorized)
			return
		}
		http.SetCookie(writer, &http.Cookie{Name: "session", Value: "authenticated"})
		writer.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/account", func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie("session")
		if err != nil || cookie.Value != "authenticated" {
			_, _ = writer.Write([]byte("<html><title>Login</title></html>"))
			return
		}
		_, _ = writer.Write([]byte("<html><title>Account Dashboard</title></html>"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	// Allow private targets so the loopback httptest server is reachable, and
	// whitelist its ephemeral port (the guard only allows 80/443 by default).
	guard := security.NewURLGuard(true)
	if parsed, parseErr := url.Parse(server.URL); parseErr == nil {
		guard.AllowedPorts[parsed.Port()] = struct{}{}
	}
	checker := NewChecker(guard)
	checker.SetCipher(cipher)

	associatedData := security.AuthAssociatedData("tenant-1", "asset-1")
	encryptedUsername, err := cipher.Encrypt("user@example.com", associatedData)
	if err != nil {
		t.Fatal(err)
	}
	encryptedPassword, err := cipher.Encrypt("s3cret", associatedData)
	if err != nil {
		t.Fatal(err)
	}

	asset := domain.Asset{ID: "asset-1", TenantID: "tenant-1", URL: server.URL + "/account"}
	policy := domain.MonitorPolicy{
		TimeoutSeconds: 15, MaxResponseBytes: 1 << 20,
		Auth: &domain.AuthConfig{
			LoginURL:          server.URL + "/login",
			UsernameField:     "email",
			PasswordField:     "password",
			EncryptedUsername: encryptedUsername,
			EncryptedPassword: encryptedPassword,
		},
	}
	result, err := checker.Check(t.Context(), asset, policy)
	if err != nil {
		t.Fatal(err)
	}
	if loginCalls != 1 {
		t.Fatalf("expected 1 login call, got %d", loginCalls)
	}
	if !strings.Contains(string(result.RawHTML), "Account Dashboard") {
		t.Fatalf("expected authenticated snapshot, got %q", result.RawHTML)
	}
}

// TestCheckerRejectsPrivateLoginURL ensures the login endpoint is subject to the
// same SSRF boundary as the asset, so a private login URL fails closed.
func TestCheckerRejectsPrivateLoginURL(t *testing.T) {
	cipher := checkerTestCipher(t)
	checker := NewChecker(security.NewURLGuard(false))
	checker.SetCipher(cipher)

	associatedData := security.AuthAssociatedData("tenant-1", "asset-1")
	encryptedUsername, _ := cipher.Encrypt("u", associatedData)
	encryptedPassword, _ := cipher.Encrypt("p", associatedData)

	// The asset URL is a literal public address so it passes validation without
	// a DNS lookup; the login URL is loopback and must be rejected.
	asset := domain.Asset{ID: "asset-1", TenantID: "tenant-1", URL: "https://8.8.8.8/"}
	policy := domain.MonitorPolicy{
		TimeoutSeconds: 15, MaxResponseBytes: 1 << 20,
		Auth: &domain.AuthConfig{
			LoginURL:          "http://127.0.0.1/login",
			UsernameField:     "u",
			PasswordField:     "p",
			EncryptedUsername: encryptedUsername,
			EncryptedPassword: encryptedPassword,
		},
	}
	if _, err := checker.Check(t.Context(), asset, policy); err == nil {
		t.Fatal("expected private login URL to be rejected")
	}
}

// TestCheckerLoginRequiresCipher ensures a policy with auth fails closed when no
// cipher has been configured, rather than silently skipping the login.
func TestCheckerLoginRequiresCipher(t *testing.T) {
	checker := NewChecker(security.NewURLGuard(true)) // cipher intentionally unset
	asset := domain.Asset{ID: "asset-1", TenantID: "tenant-1", URL: "https://8.8.8.8/"}
	policy := domain.MonitorPolicy{
		TimeoutSeconds: 15, MaxResponseBytes: 1 << 20,
		Auth: &domain.AuthConfig{
			LoginURL:          "https://8.8.8.8/login",
			UsernameField:     "u",
			PasswordField:     "p",
			EncryptedUsername: "x",
			EncryptedPassword: "y",
		},
	}
	if _, err := checker.Check(t.Context(), asset, policy); err == nil {
		t.Fatal("expected failure when no cipher is configured")
	}
}
