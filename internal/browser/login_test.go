package browser

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/security"
)

func loginTestCipher(t *testing.T) *security.SecretCipher {
	t.Helper()
	cipher, err := security.NewSecretCipher(
		base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

// loginWorkerStub stands in for the browser worker and records what it was sent.
// Its guard also knows the stub's ephemeral port, because the guard only allows
// 80 and 443 by default and the login URL has to pass that same check.
type loginWorkerStub struct {
	server   *httptest.Server
	guard    *security.URLGuard
	requests []map[string]any
}

func newLoginWorkerStub(t *testing.T, allowPrivate bool) *loginWorkerStub {
	t.Helper()
	stub := &loginWorkerStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode check request: %v", err)
		}
		stub.requests = append(stub.requests, body)
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"snapshot":         domain.Snapshot{URL: "https://example.test/", StatusCode: 200},
			"htmlBase64":       base64.StdEncoding.EncodeToString([]byte("<html></html>")),
			"screenshotBase64": base64.StdEncoding.EncodeToString([]byte("png")),
		})
	}))
	t.Cleanup(stub.server.Close)

	stub.guard = security.NewURLGuard(allowPrivate)
	if parsed, err := url.Parse(stub.server.URL); err == nil {
		stub.guard.AllowedPorts[parsed.Port()] = struct{}{}
	}
	return stub
}

func (s *loginWorkerStub) calls() int { return len(s.requests) }

func (s *loginWorkerStub) testAsset() domain.Asset {
	return domain.Asset{ID: "asset-1", TenantID: "tenant-1", URL: s.server.URL + "/account"}
}

// Without a cipher the credentials cannot be decrypted, so the request must fail
// before it is sent. Rendering the login form and snapshotting it would record
// the login page as if it were the protected page.
func TestCheckFailsClosedWithoutCipherWhenLoginIsRequired(t *testing.T) {
	stub := newLoginWorkerStub(t, true)
	client := NewClient(stub.server.URL, "worker-token", stub.guard)

	_, err := client.Check(t.Context(), stub.testAsset(), domain.MonitorPolicy{
		Auth: &domain.AuthConfig{LoginURL: stub.server.URL + "/login"},
	})
	if err == nil || !strings.Contains(err.Error(), "secret cipher") {
		t.Fatalf("err = %v, want a missing-cipher failure", err)
	}
	if stub.calls() != 0 {
		t.Fatalf("the worker was called %d times without a usable cipher", stub.calls())
	}
}

// ADR-0004: the control plane decrypts and passes plaintext, so the worker can
// submit the real form instead of snapshotting the login page.
func TestCheckSendsDecryptedCredentials(t *testing.T) {
	stub := newLoginWorkerStub(t, true)
	cipher := loginTestCipher(t)
	associatedData := security.AuthAssociatedData("tenant-1", "asset-1")
	encryptedUsername, err := cipher.Encrypt("user@example.com", associatedData)
	if err != nil {
		t.Fatal(err)
	}
	encryptedPassword, err := cipher.Encrypt("s3cret", associatedData)
	if err != nil {
		t.Fatal(err)
	}

	client := NewClient(stub.server.URL, "worker-token", stub.guard)
	client.SetCipher(cipher)
	asset := stub.testAsset()
	if _, err := client.Check(t.Context(), asset, domain.MonitorPolicy{
		Auth: &domain.AuthConfig{
			LoginURL: stub.server.URL + "/login", UsernameField: "email",
			PasswordField: "password", SubmitSelector: "#submit",
			EncryptedUsername: encryptedUsername, EncryptedPassword: encryptedPassword,
		},
	}); err != nil {
		t.Fatal(err)
	}

	if stub.calls() != 1 {
		t.Fatalf("worker calls = %d, want 1", stub.calls())
	}
	auth, ok := stub.requests[0]["auth"].(map[string]any)
	if !ok {
		t.Fatalf("check request carried no auth block: %+v", stub.requests[0])
	}
	if auth["username"] != "user@example.com" || auth["password"] != "s3cret" {
		t.Fatalf("auth carries the wrong credentials: %+v", auth)
	}
	if auth["submitSelector"] != "#submit" {
		t.Fatalf("submitSelector = %v", auth["submitSelector"])
	}
	if auth["loginUrl"] != stub.server.URL+"/login" {
		t.Fatalf("loginUrl = %v", auth["loginUrl"])
	}
}

// A policy written while ALLOW_PRIVATE_TARGETS was set, or before this path
// existed, must not be able to aim the worker at an internal address.
func TestCheckRejectsUnsafeLoginURL(t *testing.T) {
	stub := newLoginWorkerStub(t, false)
	cipher := loginTestCipher(t)
	associatedData := security.AuthAssociatedData("tenant-1", "asset-1")
	encryptedUsername, err := cipher.Encrypt("user@example.com", associatedData)
	if err != nil {
		t.Fatal(err)
	}
	encryptedPassword, err := cipher.Encrypt("s3cret", associatedData)
	if err != nil {
		t.Fatal(err)
	}

	client := NewClient(stub.server.URL, "worker-token", stub.guard)
	client.SetCipher(cipher)
	_, err = client.Check(t.Context(), stub.testAsset(), domain.MonitorPolicy{
		Auth: &domain.AuthConfig{
			LoginURL: "http://169.254.169.254/login", UsernameField: "email",
			PasswordField:     "password",
			EncryptedUsername: encryptedUsername, EncryptedPassword: encryptedPassword,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "unsafe login URL") {
		t.Fatalf("err = %v, want an unsafe login URL failure", err)
	}
	if stub.calls() != 0 {
		t.Fatalf("the worker was called %d times for an unsafe login URL", stub.calls())
	}
}

// The ciphertext is bound to one tenant and asset, so a blob copied from another
// asset must not decrypt here even with the right key.
func TestCheckRejectsCredentialsBoundToAnotherAsset(t *testing.T) {
	stub := newLoginWorkerStub(t, true)
	cipher := loginTestCipher(t)
	otherAsset := security.AuthAssociatedData("tenant-1", "asset-other")
	encryptedUsername, err := cipher.Encrypt("user@example.com", otherAsset)
	if err != nil {
		t.Fatal(err)
	}
	encryptedPassword, err := cipher.Encrypt("s3cret", otherAsset)
	if err != nil {
		t.Fatal(err)
	}

	client := NewClient(stub.server.URL, "worker-token", stub.guard)
	client.SetCipher(cipher)
	_, err = client.Check(t.Context(), stub.testAsset(), domain.MonitorPolicy{
		Auth: &domain.AuthConfig{
			LoginURL: stub.server.URL + "/login", UsernameField: "email",
			PasswordField:     "password",
			EncryptedUsername: encryptedUsername, EncryptedPassword: encryptedPassword,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "decrypt login") {
		t.Fatalf("err = %v, want a decryption failure", err)
	}
	if stub.calls() != 0 {
		t.Fatalf("the worker was called %d times with undecryptable credentials", stub.calls())
	}
}
