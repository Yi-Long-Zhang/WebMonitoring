package notify

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
	deliveries, _, err := repository.ListAlertDeliveries("tenant", "incident", store.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
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
	deliveries, _, err := repository.ListAlertDeliveries("tenant", "incident", store.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
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
	deliveries, _, err := repository.ListAlertDeliveries("tenant", "incident", store.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(map[string]string, len(deliveries))
	for _, delivery := range deliveries {
		statuses[delivery.ID] = delivery.Status
	}
	if statuses["pending"] != "FAILED" || statuses["delivered"] != "DELIVERED" {
		t.Fatalf("unexpected dead-letter statuses: %#v", statuses)
	}
}

func TestSendDingTalkChannel(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		timestamp := request.URL.Query().Get("timestamp")
		sign := request.URL.Query().Get("sign")
		if timestamp == "" || sign == "" {
			t.Error("expected dingtalk timestamp and sign query parameters")
		}
		mac := hmac.New(sha256.New, []byte("secret"))
		_, _ = mac.Write([]byte(timestamp + "\n" + "secret"))
		if sign != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
			t.Error("dingtalk signature mismatch")
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	repository := store.NewMemoryRepository()
	guard := security.NewURLGuard(true)
	port := server.Listener.Addr().(*net.TCPAddr).Port
	guard.AllowedPorts[strconv.Itoa(port)] = struct{}{}
	if err := repository.CreateAlertChannel(domain.AlertChannel{
		ID: "channel", TenantID: "tenant", Type: "DINGTALK", URL: server.URL,
		Secret: "secret", Enabled: true, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(repository, guard)
	incident := domain.Incident{ID: "incident", TenantID: "tenant", AssetID: "asset", Severity: domain.SeverityHigh, Title: "changed"}
	asset := domain.Asset{ID: "asset", TenantID: "tenant", Name: "site"}

	if failures := dispatcher.SendIncident(t.Context(), incident, asset); len(failures) != 0 {
		t.Fatalf("dingtalk delivery failed: %v", failures)
	}
	if payload["msgtype"] != "markdown" {
		t.Fatalf("unexpected dingtalk payload: %v", payload)
	}
}

func startFakeSMTP(t *testing.T) (string, chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan string, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleSMTPConnection(conn, received)
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return listener.Addr().String(), received
}

func handleSMTPConnection(conn net.Conn, received chan string) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	write := func(line string) {
		_, _ = writer.WriteString(line + "\r\n")
		_ = writer.Flush()
	}
	write("220 fake ESMTP")
	inData := false
	var data []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				inData = false
				received <- strings.Join(data, "\n")
				data = nil
				write("250 OK")
			} else {
				data = append(data, line)
			}
			continue
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			write("250-fake")
			write("250 8BITMIME")
		case strings.HasPrefix(upper, "MAIL FROM"), strings.HasPrefix(upper, "RCPT TO"):
			write("250 OK")
		case strings.HasPrefix(upper, "DATA"):
			inData = true
			write("354 go ahead")
		case strings.HasPrefix(upper, "QUIT"):
			write("221 bye")
			return
		default:
			write("250 OK")
		}
	}
}

func TestSendEmailChannel(t *testing.T) {
	addr, received := startFakeSMTP(t)
	host, portString, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatal(err)
	}

	repository := store.NewMemoryRepository()
	guard := security.NewURLGuard(true)
	if err := repository.CreateAlertChannel(domain.AlertChannel{
		ID: "channel", TenantID: "tenant", Type: "EMAIL",
		Recipients: "a@example.com, b@example.com", Enabled: true, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(repository, guard)
	dispatcher.SetSMTP(SMTPConfig{Host: host, Port: port, From: "alerts@example.com"})
	incident := domain.Incident{ID: "incident", TenantID: "tenant", AssetID: "asset", Severity: domain.SeverityHigh, Title: "changed"}
	asset := domain.Asset{ID: "asset", TenantID: "tenant", Name: "site"}

	if failures := dispatcher.SendIncident(t.Context(), incident, asset); len(failures) != 0 {
		t.Fatalf("email delivery failed: %v", failures)
	}
	select {
	case message := <-received:
		if !strings.Contains(message, "changed") || !strings.Contains(message, "a@example.com") {
			t.Fatalf("unexpected email body: %q", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no email received from fake SMTP server")
	}
}

func TestSendEmailChannelWithoutSMTPConfig(t *testing.T) {
	repository := store.NewMemoryRepository()
	guard := security.NewURLGuard(true)
	if err := repository.CreateAlertChannel(domain.AlertChannel{
		ID: "channel", TenantID: "tenant", Type: "EMAIL",
		Recipients: "a@example.com", Enabled: true, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(repository, guard)
	incident := domain.Incident{ID: "incident", TenantID: "tenant", AssetID: "asset"}
	asset := domain.Asset{ID: "asset", TenantID: "tenant"}

	if failures := dispatcher.SendIncident(t.Context(), incident, asset); len(failures) == 0 {
		t.Fatal("expected email delivery to fail without SMTP configuration")
	}
}
