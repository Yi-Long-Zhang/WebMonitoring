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
