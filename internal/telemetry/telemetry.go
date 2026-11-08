// Package telemetry wires OpenTelemetry tracing and the JSON logger.
package telemetry

import (
	"context"
	"io"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// ServiceName is reported on traces unless OTEL_SERVICE_NAME overrides it.
const ServiceName = "burnrate"

// NewLogger returns a JSON slog logger writing to w.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// TracingEnabled reports whether an OTLP endpoint is configured.
func TracingEnabled(getenv func(string) string) bool {
	return getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

// SetupTracing installs the global propagator and, when an OTLP endpoint is
// configured through the standard OTEL_* variables, a batching tracer
// provider. The returned function flushes and stops it.
func SetupTracing(ctx context.Context, version string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	if !TracingEnabled(os.Getenv) {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	// Plain attribute keys instead of a pinned semconv package: merging a
	// semconv schema URL that differs from the SDK default fails at startup.
	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			attribute.String("service.name", ServiceName),
			attribute.String("service.version", version),
		),
		// OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES win over the defaults.
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// TraceID returns the hex trace ID in ctx, or "" when there is none.
func TraceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}
