package api

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"log/slog"

	"webmonitoring/internal/auth"
	"webmonitoring/internal/detection"
	"webmonitoring/internal/domain"
	"webmonitoring/internal/events"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/notify"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

func newStreamTestServer(t *testing.T) (*Server, *events.Broker) {
	t.Helper()
	repository := store.NewMemoryRepository()
	hash, err := security.HashPassword("a-long-password-for-testing")
	if err != nil {
		t.Fatal(err)
	}
	user := domain.User{
		ID: identity.New(), TenantID: defaultTenant, Username: "admin",
		PasswordHash: hash, Role: domain.RoleAdmin, CreatedAt: time.Now().UTC(),
	}
	if err := repository.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	guard := security.NewURLGuard(false)
	broker := events.NewBroker()
	checker := detection.NewChecker(guard)
	service := detection.NewService(
		repository, evidenceStore, checker, nil, notify.NewDispatcher(repository, guard), broker,
		slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)),
	)
	server := NewServer(repository, auth.NewService(repository, time.Hour), service, evidenceStore, guard, broker,
		slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)), false)
	return server, broker
}

// TestStreamEventsSurvivesPastServerWriteTimeout reproduces the production
// topology: the API server sets an absolute per-request WriteTimeout
// (2 minutes), which used to silently kill every SSE stream at that mark.
// Here the timeout is squeezed to 400ms and an update is published after it
// has passed; the stream must still deliver it.
func TestStreamEventsSurvivesPastServerWriteTimeout(t *testing.T) {
	server, broker := newStreamTestServer(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      400 * time.Millisecond,
	}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close() })
	address := "http://" + listener.Addr().String()

	login, err := http.Post(address+"/api/v1/auth/session", "application/json",
		bytes.NewBufferString(`{"username":"admin","password":"a-long-password-for-testing"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer login.Body.Close()
	if login.StatusCode != http.StatusCreated {
		t.Fatalf("login status: %d", login.StatusCode)
	}
	cookies := login.Cookies()
	if len(cookies) == 0 {
		t.Fatal("login returned no session cookie")
	}

	request, err := http.NewRequest(http.MethodGet, address+"/api/v1/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookies[0])
	stream, err := (&http.Client{}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("stream status: %d", stream.StatusCode)
	}

	received := make(chan string, 8)
	go func() {
		reader := bufio.NewReader(stream.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				close(received)
				return
			}
			if name, ok := strings.CutPrefix(strings.TrimSpace(line), "event: "); ok {
				received <- name
			}
		}
	}()

	if got := <-received; got != "ready" {
		t.Fatalf("expected ready event, got %q", got)
	}

	// Pass the server's absolute WriteTimeout before anything else is written.
	time.Sleep(600 * time.Millisecond)
	broker.Publish("incident.created", map[string]any{"id": "incident-1"})

	select {
	case got, ok := <-received:
		if !ok {
			t.Fatal("stream closed before delivering the post-deadline event")
		}
		if got != "update" {
			t.Fatalf("expected update event, got %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no update event received after the server's WriteTimeout had passed; the stream died at the absolute deadline")
	}
}

type failingStreamWriter struct {
	header http.Header
}

func (w *failingStreamWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *failingStreamWriter) WriteHeader(int) {}

func (w *failingStreamWriter) Write([]byte) (int, error) {
	return 0, errors.New("connection reset by peer")
}

func (w *failingStreamWriter) Flush() {}

// TestStreamEventsStopsOnWriteFailure pins the other half of the fix: a write
// failure (dead client, expired deadline) must end the handler so the
// subscription is released, instead of spinning on a broken connection.
func TestStreamEventsStopsOnWriteFailure(t *testing.T) {
	server := &Server{broker: events.NewBroker()}
	request, err := http.NewRequest(http.MethodGet, "/api/v1/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		server.streamEvents(&failingStreamWriter{}, request)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("streamEvents kept running after the first write failed")
	}
}
