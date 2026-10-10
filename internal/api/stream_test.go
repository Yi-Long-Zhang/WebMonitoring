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
	broker.Publish(defaultTenant, "incident.created", map[string]any{"id": "incident-1"})

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
	// A principal has to be present or the tenant guard returns before the first
	// write, and this test would pass without ever exercising the write path it
	// exists to cover.
	request = request.WithContext(context.WithValue(request.Context(), principalKey{},
		auth.Session{User: domain.User{TenantID: defaultTenant}}))

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

// tenantBID is a second tenant, distinct from defaultTenant. It exists so the
// stream tests can prove an event reaches exactly one tenant.
const tenantBID = "00000000-0000-4000-8000-000000000002"

// startStreamServer serves the API on a loopback port, so the tests below drive
// the real SSE path -- cookies, streaming, the flusher -- rather than a recorder
// that cannot stream.
func startStreamServer(t *testing.T, server *Server) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: server, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close() })
	return "http://" + listener.Addr().String()
}

// loginOverHTTP signs in over the wire so the cookie is the real one. The body
// carries the optional tenant slug, which is what selects a non-default tenant.
func loginOverHTTP(t *testing.T, address, body string) *http.Cookie {
	t.Helper()
	response, err := http.Post(address+"/api/v1/auth/session", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close() //nolint:errcheck // 测试收尾，失败不影响断言
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("login status: %d", response.StatusCode)
	}
	cookies := response.Cookies()
	if len(cookies) == 0 {
		t.Fatal("login returned no session cookie")
	}
	return cookies[0]
}

// openStream opens an SSE connection and reports the event names it receives.
func openStream(t *testing.T, address string, cookie *http.Cookie) (<-chan string, func()) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, address+"/api/v1/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookie)
	stream, err := (&http.Client{}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if stream.StatusCode != http.StatusOK {
		_ = stream.Body.Close() //nolint:errcheck // 上面已经失败，这里只是收尾
		t.Fatalf("stream status: %d", stream.StatusCode)
	}
	events := make(chan string, 16)
	go func() {
		defer close(events)
		reader := bufio.NewReader(stream.Body)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				return
			}
			if name, ok := strings.CutPrefix(strings.TrimSpace(line), "event: "); ok {
				events <- name
			}
		}
	}()
	return events, func() { _ = stream.Body.Close() } //nolint:errcheck // 测试收尾
}

func expectEvent(t *testing.T, events <-chan string, want, who string) {
	t.Helper()
	select {
	case got, open := <-events:
		if !open {
			t.Fatalf("%s: the stream closed before the event arrived", who)
		}
		if got != want {
			t.Fatalf("%s: expected %q, got %q", who, want, got)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: no %q event arrived", who, want)
	}
}

func expectNoEvent(t *testing.T, events <-chan string, message string) {
	t.Helper()
	select {
	case got := <-events:
		t.Fatalf("%s: received %q", message, got)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestStreamEventsAreScopedToTenant is the negative test for the leak. Before
// the broker was scoped to a tenant, any authenticated user -- an AUDITOR
// included -- received every other tenant's incident transitions, carrying
// tenantId, title, signals and evidenceIds. Opening two streams and publishing
// to one tenant is what proves the scoping, in both directions.
func TestStreamEventsAreScopedToTenant(t *testing.T) {
	server, broker := newStreamTestServer(t)
	repository, ok := server.repository.(*store.MemoryRepository)
	if !ok {
		t.Fatalf("newStreamTestServer built a %T, want *store.MemoryRepository", server.repository)
	}
	retentionUserInTenant(t, repository, tenantBID, "tenantb-admin", domain.RoleAdmin)

	address := startStreamServer(t, server)
	ownerCookie := loginOverHTTP(t, address,
		`{"username":"admin","password":"a-long-password-for-testing"}`)
	otherCookie := loginOverHTTP(t, address,
		`{"tenant":"ttenantb-admin","username":"tenantb-admin","password":"a-long-password-for-testing"}`)

	ownerEvents, closeOwner := openStream(t, address, ownerCookie)
	defer closeOwner()
	otherEvents, closeOther := openStream(t, address, otherCookie)
	defer closeOther()

	if got := <-ownerEvents; got != "ready" {
		t.Fatalf("owner stream: expected ready, got %q", got)
	}
	if got := <-otherEvents; got != "ready" {
		t.Fatalf("other stream: expected ready, got %q", got)
	}

	broker.Publish(defaultTenant, "incident.created", map[string]any{"id": "owner-incident"})
	expectEvent(t, ownerEvents, "update", "the publishing tenant")
	expectNoEvent(t, otherEvents, "another tenant's stream received the event")

	broker.Publish(tenantBID, "incident.created", map[string]any{"id": "other-incident"})
	expectEvent(t, otherEvents, "update", "the second publishing tenant")
	expectNoEvent(t, ownerEvents, "the first tenant's stream received the second tenant's event")
}
