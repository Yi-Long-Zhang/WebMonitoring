package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCORSSameOriginByDefault pins the default: with no allowlist configured the
// API tells no origin it may read a response, and no preflight is answered.
// This is the single-host Compose topology, where the console and the API share
// an origin and CORS never enters the picture -- a regression that started
// echoing every Origin would widen the API for deployments that never asked.
func TestCORSSameOriginByDefault(t *testing.T) {
	_, server, _ := verificationServer(t, fakeResolver{txt: map[string][]string{}})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	request.Header.Set("Origin", "https://evil.example")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("an unconfigured server echoed an origin: %q", got)
	}

	preflight := httptest.NewRequest(http.MethodOptions, "/api/v1/dashboard", nil)
	preflight.Header.Set("Origin", "https://evil.example")
	preflight.Header.Set("Access-Control-Request-Method", "GET")
	preflightResponse := httptest.NewRecorder()
	server.ServeHTTP(preflightResponse, preflight)

	if preflightResponse.Code != http.StatusForbidden {
		t.Fatalf("preflight from an unlisted origin status=%d", preflightResponse.Code)
	}
}

// TestCORSAnswersListedOriginWithCredentials is the case a cross-origin console
// depends on. The origin is echoed rather than answered with "*", and
// Allow-Credentials is set, because the session cookie is what authenticates
// every call -- a wildcard would make the browser drop the cookie and turn every
// request into a 401.
func TestCORSAnswersListedOriginWithCredentials(t *testing.T) {
	_, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	server.SetAllowedOrigins(map[string]struct{}{"https://console.example": {}})
	cookie, _ := loginForTest(t, server, admin.Username)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	request.Header.Set("Origin", "https://console.example")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("listed origin status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://console.example" {
		t.Fatalf("Allow-Origin=%q, want the echoed origin", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Allow-Credentials=%q, want true", got)
	}
	// Vary matters on the denied path too: a cache that stored the un-CORS'd
	// response must not hand it to the allowed origin, or the reverse.
	if got := response.Header().Values("Vary"); len(got) == 0 {
		t.Fatal("Vary: Origin missing, so a shared cache could cross origins")
	}

	// An origin not on the list still gets no CORS headers even though the
	// request itself is authenticated and succeeds.
	other := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	other.Header.Set("Origin", "https://other.example")
	other.AddCookie(cookie)
	otherResponse := httptest.NewRecorder()
	server.ServeHTTP(otherResponse, other)
	if got := otherResponse.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unlisted origin was echoed: %q", got)
	}
}

// TestCORSPrerespondsWithoutCredentials pins why the preflight is answered in
// the middleware instead of by the handler: a browser sends OPTIONS without the
// session cookie, so a preflight routed through the auth check would 401 and the
// real request would never be sent.
func TestCORSPreflightSkipsAuth(t *testing.T) {
	_, server, _ := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	server.SetAllowedOrigins(map[string]struct{}{"https://console.example": {}})

	preflight := httptest.NewRequest(http.MethodOptions, "/api/v1/assets", nil)
	preflight.Header.Set("Origin", "https://console.example")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, preflight)

	if response.Code != http.StatusNoContent {
		t.Fatalf("preflight status=%d, want 204 without a session", response.Code)
	}
	if got := response.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatal("preflight did not advertise the allowed methods")
	}
	// The CSRF header is what the console sends on every write, so a preflight
	// that did not allow it would block every write from a cross-origin console.
	if got := response.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatal("preflight did not advertise the allowed headers")
	}
}

// TestHSTSOnlyOnSecureDeployments pins that the header follows the session
// cookie's Secure flag. Sending HSTS on a plaintext deployment would pin the
// browser to https for a host that does not serve it, breaking the console
// entirely rather than improving it.
func TestHSTSOnlyOnSecureDeployments(t *testing.T) {
	_, server, _ := verificationServer(t, fakeResolver{txt: map[string][]string{}})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if got := response.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("plaintext deployment sent HSTS: %q", got)
	}

	server.sessionSecure = true
	secureRequest := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	secureResponse := httptest.NewRecorder()
	server.ServeHTTP(secureResponse, secureRequest)
	if got := secureResponse.Header().Get("Strict-Transport-Security"); got == "" {
		t.Fatal("TLS deployment did not send HSTS")
	}
}
