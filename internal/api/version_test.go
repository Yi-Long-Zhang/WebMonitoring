package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"time"
	"webmonitoring/internal/domain"
	"webmonitoring/internal/version"
)

// The build identity is troubleshooting material: it answers "which version is
// actually running", which is the first question of any incident. It is behind
// the session gate because handing an unauthenticated caller the exact version
// of a deployment is how they pick which known issue to try.
func TestVersionEndpointReportsTheBuildIdentity(t *testing.T) {
	server, cookie, _, _, _ := escalationServer(t, domain.SeverityLow, domain.IncidentOpen, "", time.Second)

	anonymous := httptest.NewRecorder()
	server.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d, want 401", anonymous.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var info version.Info
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Version == "" || info.Commit == "" || info.BuildTime == "" || info.GoVersion == "" {
		t.Fatalf("the endpoint left a field empty: %+v", info)
	}
}
