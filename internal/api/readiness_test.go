package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"webmonitoring/internal/auth"
	"webmonitoring/internal/detection"
	"webmonitoring/internal/events"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/notify"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

func TestHealthAndReadinessEndpoints(t *testing.T) {
	repository := store.NewMemoryRepository()
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	guard := security.NewURLGuard(false)
	broker := events.NewBroker()
	service := detection.NewService(
		repository, evidenceStore, detection.NewChecker(guard), nil,
		notify.NewDispatcher(repository, guard), broker,
		slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)),
	)
	server := NewServer(repository, auth.NewService(repository, time.Hour), service, evidenceStore,
		guard, broker, slog.Default(), false)

	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s returned %d", path, response.Code)
		}
	}

	server.SetReadiness(func(_ context.Context) map[string]error {
		return map[string]error{"postgres": errors.New("unavailable")}
	})
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "postgres") {
		t.Fatalf("unexpected readiness response: %d %s", response.Code, response.Body.String())
	}
}
