package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/security"
)

func testAsset() domain.Asset {
	return domain.Asset{ID: "asset-1", TenantID: "tenant-1", URL: "https://example.test/"}
}

func TestAvailableRequiresURLAndToken(t *testing.T) {
	cases := map[string]struct {
		baseURL string
		token   string
		want    bool
	}{
		"configured":   {"http://worker:8081", "worker-token", true},
		"no token":     {"http://worker:8081", "", false},
		"no url":       {"", "worker-token", false},
		"unconfigured": {"", "", false},
	}
	for name, testCase := range cases {
		client := NewClient(testCase.baseURL, testCase.token, security.NewURLGuard(false))
		if got := client.Available(); got != testCase.want {
			t.Errorf("%s: Available() = %v, want %v", name, got, testCase.want)
		}
	}
}

func TestCheckWithoutConfigurationFailsClosed(t *testing.T) {
	client := NewClient("", "", security.NewURLGuard(false))
	if _, err := client.Check(t.Context(), testAsset(), domain.MonitorPolicy{}); err == nil {
		t.Fatal("an unconfigured browser worker must not be used")
	}
	if err := client.Ready(t.Context()); err == nil {
		t.Fatal("an unconfigured browser worker must not report ready")
	}
}

func TestCheckDecodesSnapshotAndEvidence(t *testing.T) {
	html := []byte("<html><body>hello</body></html>")
	screenshot := []byte{0x89, 'P', 'N', 'G'}

	var received struct {
		URL                 string `json:"url"`
		TimeoutSeconds      int    `json:"timeoutSeconds"`
		AllowPrivateTargets bool   `json:"allowPrivateTargets"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/check" {
			t.Errorf("unexpected path %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer worker-token" {
			t.Errorf("Authorization = %q", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode check request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"snapshot": domain.Snapshot{
				URL: "https://example.test/", StatusCode: 200, Title: "hello",
			},
			"htmlBase64":       base64.StdEncoding.EncodeToString(html),
			"screenshotBase64": base64.StdEncoding.EncodeToString(screenshot),
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "worker-token", security.NewURLGuard(true))
	result, err := client.Check(t.Context(), testAsset(), domain.MonitorPolicy{TimeoutSeconds: 45})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.RawHTML) != string(html) {
		t.Fatalf("RawHTML = %q", result.RawHTML)
	}
	if string(result.Screenshot) != string(screenshot) {
		t.Fatalf("Screenshot = %v", result.Screenshot)
	}
	if result.Snapshot.Title != "hello" || result.Snapshot.StatusCode != 200 {
		t.Fatalf("snapshot = %+v", result.Snapshot)
	}
	if received.URL != "https://example.test/" || received.TimeoutSeconds != 45 || !received.AllowPrivateTargets {
		t.Fatalf("worker received %+v", received)
	}
}

func TestCheckRejectsWorkerErrors(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusBadRequest, http.StatusInternalServerError, http.StatusBadGateway} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
		}))
		client := NewClient(server.URL, "worker-token", security.NewURLGuard(false))
		_, err := client.Check(t.Context(), testAsset(), domain.MonitorPolicy{})
		server.Close()
		if err == nil {
			t.Fatalf("status %d must be reported as an error", status)
		}
		if !strings.Contains(err.Error(), "browser worker returned") {
			t.Fatalf("status %d: unexpected error %v", status, err)
		}
	}
}

func TestCheckRejectsMalformedWorkerResponses(t *testing.T) {
	cases := map[string]string{
		"not json":            `{"snapshot":`,
		"bad html base64":     `{"snapshot":{},"htmlBase64":"!!!not-base64!!!","screenshotBase64":""}`,
		"bad screenshot data": `{"snapshot":{},"htmlBase64":"aGk=","screenshotBase64":"%%%"}`,
	}
	for name, body := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(body))
		}))
		client := NewClient(server.URL, "worker-token", security.NewURLGuard(false))
		_, err := client.Check(t.Context(), testAsset(), domain.MonitorPolicy{})
		server.Close()
		if err == nil {
			t.Fatalf("%s: a malformed response must not produce a result", name)
		}
	}
}

func TestCheckHonoursContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewClient(server.URL, "worker-token", security.NewURLGuard(false))
	if _, err := client.Check(ctx, testAsset(), domain.MonitorPolicy{}); err == nil {
		t.Fatal("a cancelled context must abort the call")
	}
}

func TestReadyReportsWorkerHealth(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/readyz" {
			t.Errorf("unexpected path %q", request.URL.Path)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer healthy.Close()
	if err := NewClient(healthy.URL, "worker-token", security.NewURLGuard(false)).Ready(t.Context()); err != nil {
		t.Fatalf("healthy worker reported: %v", err)
	}

	unhealthy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unhealthy.Close()
	if err := NewClient(unhealthy.URL, "worker-token", security.NewURLGuard(false)).Ready(t.Context()); err == nil {
		t.Fatal("an unhealthy worker must not report ready")
	}
}
