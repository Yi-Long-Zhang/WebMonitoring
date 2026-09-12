package notify

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

func TestSendIncidentPersistsAndDeduplicatesDelivery(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Header.Get("X-WebMonitoring-Signature") == "" {
			t.Error("expected webhook signature")
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repository := store.NewMemoryRepository()
	guard := security.NewURLGuard(true)
	port := server.Listener.Addr().(*net.TCPAddr).Port
	guard.AllowedPorts[strconv.Itoa(port)] = struct{}{}
	channel := domain.AlertChannel{
		ID: "channel", TenantID: "tenant", Type: "WEBHOOK", URL: server.URL,
		Secret: "secret", Enabled: true, CreatedAt: time.Now().UTC(),
	}
	if err := repository.CreateAlertChannel(channel); err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(repository, guard)
	incident := domain.Incident{ID: "incident", TenantID: "tenant", AssetID: "asset", Severity: domain.SeverityHigh, Title: "changed"}
	asset := domain.Asset{ID: "asset", TenantID: "tenant", Name: "site"}

	if failures := dispatcher.SendIncident(t.Context(), incident, asset); len(failures) != 0 {
		t.Fatalf("first delivery failed: %v", failures)
	}
	if failures := dispatcher.SendIncident(t.Context(), incident, asset); len(failures) != 0 {
		t.Fatalf("duplicate delivery failed: %v", failures)
	}
	if requests.Load() != 1 {
		t.Fatalf("expected one webhook request, got %d", requests.Load())
	}
	deliveries := repository.ListAlertDeliveries("tenant", "incident")
	if len(deliveries) != 1 || deliveries[0].Status != "DELIVERED" || deliveries[0].Attempts != 1 {
		t.Fatalf("unexpected delivery record: %#v", deliveries)
	}
}

func TestSendIncidentRetriesWithPersistedAttemptCount(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) < 3 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repository := store.NewMemoryRepository()
	guard := security.NewURLGuard(true)
	port := server.Listener.Addr().(*net.TCPAddr).Port
	guard.AllowedPorts[strconv.Itoa(port)] = struct{}{}
	if err := repository.CreateAlertChannel(domain.AlertChannel{
		ID: "channel", TenantID: "tenant", Type: "WEBHOOK", URL: server.URL,
		Enabled: true, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(repository, guard)
	dispatcher.SetRetryPolicy(3, time.Millisecond)
	incident := domain.Incident{ID: "incident", TenantID: "tenant", AssetID: "asset"}
	asset := domain.Asset{ID: "asset", TenantID: "tenant"}

	if failures := dispatcher.SendIncident(t.Context(), incident, asset); len(failures) != 0 {
		t.Fatalf("delivery failed: %v", failures)
	}
	deliveries := repository.ListAlertDeliveries("tenant", "incident")
	if len(deliveries) != 1 || deliveries[0].Status != "DELIVERED" || deliveries[0].Attempts != 3 {
		t.Fatalf("unexpected delivery record: %#v", deliveries)
	}
	if deliveries[0].NextAttemptAt != nil {
		t.Fatal("successful delivery retained a next attempt time")
	}
}

func TestMarkDeadLetterFinalizesPendingDeliveries(t *testing.T) {
	repository := store.NewMemoryRepository()
	now := time.Now().UTC()
	for _, delivery := range []domain.AlertDelivery{
		{ID: "pending", TenantID: "tenant", IncidentID: "incident", ChannelID: "one", DedupKey: "one", Status: "PENDING", CreatedAt: now},
		{ID: "delivered", TenantID: "tenant", IncidentID: "incident", ChannelID: "two", DedupKey: "two", Status: "DELIVERED", CreatedAt: now},
	} {
		if err := repository.CreateAlertDelivery(delivery); err != nil {
			t.Fatal(err)
		}
	}
	dispatcher := NewDispatcher(repository, security.NewURLGuard(true))
	if err := dispatcher.MarkDeadLetter(t.Context(), domain.AlertJob{TenantID: "tenant", IncidentID: "incident"}); err != nil {
		t.Fatal(err)
	}
	deliveries := repository.ListAlertDeliveries("tenant", "incident")
	statuses := make(map[string]string, len(deliveries))
	for _, delivery := range deliveries {
		statuses[delivery.ID] = delivery.Status
	}
	if statuses["pending"] != "FAILED" || statuses["delivered"] != "DELIVERED" {
		t.Fatalf("unexpected dead-letter statuses: %#v", statuses)
	}
}
