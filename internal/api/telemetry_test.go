package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"webmonitoring/internal/evidence"
	"webmonitoring/internal/store"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// The OTel global provider swaps the delegate of tracers created before it was
// installed exactly once (internal/global/delegateTraceOnce). The package-level
// apiTracer is created at init, so only the first SetTracerProvider in this
// process reaches it — which is precisely why setup runs before the server
// starts serving. The helpers below respect that by installing their recording
// provider a single time and then inspecting only the spans a test caused.
var (
	recordingProviderOnce sync.Once
	recordedSpans         = tracetest.NewSpanRecorder()
)

func installRecordingProvider() {
	recordingProviderOnce.Do(func() {
		otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recordedSpans)))
	})
}

func TestSpanNameHidesTheSignedEvidenceToken(t *testing.T) {
	const token = "ZXhiYzEyMy1zaWduZWQtdG9rZW4"
	name := spanName(http.MethodGet, "/api/v1/evidence/signed/"+token)
	if strings.Contains(name, token) {
		t.Fatalf("span name leaks the signed token: %q", name)
	}
	if want := "GET /api/v1/evidence/signed/{token}"; name != want {
		t.Fatalf("span name = %q, want %q", name, want)
	}
	if got := spanName(http.MethodGet, "/api/v1/assets"); got != "GET /api/v1/assets" {
		t.Fatalf("ordinary path was rewritten: %q", got)
	}
}

func TestTraceMiddlewareAlwaysAssignsARequestID(t *testing.T) {
	repository := store.NewMemoryRepository()
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("healthz status=%d", response.Code)
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("every response must carry an X-Request-ID, tracing or not")
	}
}

func TestTraceMiddlewareExportsAServerSpan(t *testing.T) {
	installRecordingProvider()

	repository := store.NewMemoryRepository()
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)

	before := len(recordedSpans.Ended())
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("healthz status=%d", response.Code)
	}

	ended := recordedSpans.Ended()
	if len(ended) != before+1 {
		t.Fatalf("request exported %d spans, want 1", len(ended)-before)
	}
	span := ended[len(ended)-1]
	if span.Name() != "GET /healthz" {
		t.Fatalf("span name = %q, want %q", span.Name(), "GET /healthz")
	}
	if !span.SpanContext().IsValid() {
		t.Fatal("exported span has no valid span context")
	}
	// The trace ID a caller sees must be the exported trace ID, otherwise an
	// audit record carrying X-Request-ID cannot be joined to the trace.
	if header := response.Header().Get("X-Request-ID"); header != span.SpanContext().TraceID().String() {
		t.Fatalf("X-Request-ID = %q, want the trace ID %q", header, span.SpanContext().TraceID())
	}
	attributes := map[string]any{}
	for _, kv := range span.Attributes() {
		attributes[string(kv.Key)] = kv.Value.AsInterface()
	}
	if got := attributes["http.response.status_code"]; got != int64(http.StatusOK) {
		t.Fatalf("http.response.status_code = %v, want 200", got)
	}
}
