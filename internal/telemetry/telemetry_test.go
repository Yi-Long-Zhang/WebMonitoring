package telemetry

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestEnabledFollowsTheEndpointVariable(t *testing.T) {
	empty := func(string) string { return "" }
	if Enabled(empty) {
		t.Fatal("tracing must stay off when the endpoint is unset")
	}
	if Enabled(func(string) string { return "   " }) {
		t.Fatal("a whitespace-only endpoint must not enable tracing")
	}
	if !Enabled(func(string) string { return "http://collector:4318" }) {
		t.Fatal("a configured endpoint must enable tracing")
	}
}

func TestSetupWithoutEndpointIsNoop(t *testing.T) {
	shutdown, err := Setup(context.Background(), "webmonitoring-test", func(string) string { return "" }, testLogger())
	if err != nil {
		t.Fatalf("disabled setup returned an error: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown returned an error: %v", err)
	}
	// Both binaries defer the shutdown with the signal context, which is already
	// cancelled by the time it runs. That must not turn teardown into an error.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := shutdown(cancelled); err != nil {
		t.Fatalf("no-op shutdown rejected a cancelled context: %v", err)
	}
}

func TestSetupExportsSpansToTheConfiguredCollector(t *testing.T) {
	var (
		mu     sync.Mutex
		paths  []string
		bodies [][]byte
	)
	collector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		mu.Lock()
		paths = append(paths, request.URL.Path)
		bodies = append(bodies, body)
		mu.Unlock()
		writer.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	// The OTLP exporter reads the process environment itself, so the fake must
	// reach os.Getenv as well as the injected lookup.
	t.Setenv(EnvEndpoint, collector.URL)

	previous := otel.GetTracerProvider()
	defer otel.SetTracerProvider(previous)

	shutdown, err := Setup(context.Background(), "webmonitoring-test", os.Getenv, testLogger())
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, span := otel.Tracer("telemetry-test").Start(context.Background(), "unit-span")
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(paths) == 0 {
		t.Fatal("shutdown exported nothing to the collector")
	}
	for index, path := range paths {
		if path != "/v1/traces" {
			t.Fatalf("export %d went to %q, want /v1/traces", index, path)
		}
	}
	if !bytes.Contains(bodies[0], []byte("webmonitoring-test")) {
		t.Fatal("exported payload does not carry the configured service name")
	}
}

func TestSampleRatioFallsBackWhenOutOfRange(t *testing.T) {
	cases := map[string]float64{
		"":       1,
		"0.25":   0.25,
		"0":      0,
		"1":      1,
		"nope":   1,
		"-1":     1,
		"1.5":    1,
		"  0.5 ": 0.5,
	}
	for raw, want := range cases {
		get := func(string) string { return raw }
		if got := sampleRatio(get); got != want {
			t.Errorf("sampleRatio(%q) = %v, want %v", raw, got, want)
		}
	}
}
