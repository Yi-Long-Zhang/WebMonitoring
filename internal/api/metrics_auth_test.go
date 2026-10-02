package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The /metrics endpoint is on the unauthenticated root mux while the compose
// topology publishes the API port to the host, so an unset token must keep it
// open (development default) and a configured token must gate it.
func TestMetricsEndpointTokenGate(t *testing.T) {
	server, _ := newStreamTestServer(t)

	open := httptest.NewRecorder()
	server.ServeHTTP(open, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if open.Code != http.StatusOK {
		t.Fatalf("metrics without a configured token: status %d, want 200", open.Code)
	}

	server.SetMetricsToken("test-metrics-token-0123456789abcdef")

	cases := []struct {
		name          string
		authorization string
		want          int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer someone-else-token", http.StatusUnauthorized},
		{"wrong scheme", "Basic dXNlcjpwYXNz", http.StatusUnauthorized},
		{"valid token", "Bearer test-metrics-token-0123456789abcdef", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			if tc.authorization != "" {
				request.Header.Set("Authorization", tc.authorization)
			}
			server.ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Fatalf("status %d, want %d (body %s)", recorder.Code, tc.want, recorder.Body.String())
			}
		})
	}
}
