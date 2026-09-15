// Package telemetry wires OpenTelemetry tracing into the control plane and the
// workers.
//
// Tracing is opt-in and off by default: when OTEL_EXPORTER_OTLP_ENDPOINT is
// unset no exporter is created, the global tracer provider stays the SDK no-op
// provider, and every span call costs a few nanoseconds. Nothing here changes
// behaviour or requires configuration to run.
package telemetry

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// EnvEndpoint is the standard OTLP/HTTP collector endpoint. Its presence is what
// turns tracing on.
const EnvEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"

// Shutdown is the no-op teardown returned when tracing is disabled.
type Shutdown func(context.Context) error

// Enabled reports whether an OTLP endpoint is configured.
func Enabled(get func(string) string) bool {
	return strings.TrimSpace(get(EnvEndpoint)) != ""
}

// Setup installs a tracer provider and W3C trace-context propagator.
//
// When tracing is disabled it returns a no-op shutdown and a nil error, so
// callers can defer the result unconditionally.
func Setup(ctx context.Context, serviceName string, get func(string) string, logger *slog.Logger) (Shutdown, error) {
	if !Enabled(get) {
		logger.Info("tracing disabled", "reason", EnvEndpoint+" is not set")
		return func(context.Context) error { return nil }, nil
	}
	// The exporter reads OTEL_EXPORTER_OTLP_* itself (endpoint, headers, TLS,
	// compression), so nothing is hard-coded here. A failure to build it must
	// not take the process down: tracing is observability, not a dependency.
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	serviceVersion := strings.TrimSpace(get("APP_VERSION"))
	if serviceVersion == "" {
		serviceVersion = "dev"
	}
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		// A partially-built resource is still usable; record and continue.
		logger.Warn("merge telemetry resource attributes", "error", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRatio(get)))),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	// The endpoint value is intentionally not logged: a collector URL can carry
	// credentials or an internal hostname.
	logger.Info("tracing enabled", "service", serviceName, "version", serviceVersion)
	return func(shutdownCtx context.Context) error {
		// Callers defer this with the signal context, which is already cancelled
		// by the time a shutdown runs. WithoutCancel keeps the batch flush inside
		// its own deadline instead of aborting on an inherited cancellation.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(shutdownCtx), 5*time.Second)
		defer cancel()
		return provider.Shutdown(shutdownCtx)
	}, nil
}

// sampleRatio reads OTEL_TRACES_SAMPLER_ARG, defaulting to always-on. Out-of-range
// values fall back to the default rather than silently disabling tracing.
func sampleRatio(get func(string) string) float64 {
	raw := strings.TrimSpace(get("OTEL_TRACES_SAMPLER_ARG"))
	if raw == "" {
		return 1
	}
	ratio, err := strconv.ParseFloat(raw, 64)
	if err != nil || ratio < 0 || ratio > 1 {
		return 1
	}
	return ratio
}
