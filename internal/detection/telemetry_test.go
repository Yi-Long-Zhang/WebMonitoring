package detection

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/events"
	"webmonitoring/internal/store"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestProcessExportsAConsumerSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))

	repository := store.NewMemoryRepository()
	job := domain.DetectionJob{
		ID: "job-trace", TenantID: "tenant-trace", AssetID: "asset-trace", Mode: "HTTP",
		Status: domain.JobQueued, MaxAttempts: 1, CreatedAt: time.Now().UTC(),
	}
	if err := repository.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	// No asset is registered for the job, so the run fails fast after the span
	// is opened.
	service := NewService(
		repository, nil, nil, nil, nil, events.NewBroker(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err := service.Process(t.Context(), job); err != nil {
		t.Fatal(err)
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("process exported %d spans, want 1", len(spans))
	}
	span := spans[0]
	if span.Name() != "detection.process" {
		t.Fatalf("span name = %q, want detection.process", span.Name())
	}
	attributes := map[string]any{}
	for _, kv := range span.Attributes() {
		attributes[string(kv.Key)] = kv.Value.AsInterface()
	}
	for key, want := range map[string]any{
		"webmonitoring.tenant_id": "tenant-trace",
		"webmonitoring.job_id":    "job-trace",
		"webmonitoring.asset_id":  "asset-trace",
		"webmonitoring.mode":      "HTTP",
	} {
		if got := attributes[key]; got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
}
