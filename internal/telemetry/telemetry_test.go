package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestTracingEnabled(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if TracingEnabled(get) {
		t.Fatal("enabled with no endpoint")
	}
	env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] = "http://collector:4318/v1/traces"
	if !TracingEnabled(get) {
		t.Fatal("not enabled with traces endpoint")
	}
}

func TestSetupTracingWithoutEndpointIsNoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	shutdown, err := SetupTracing(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSetupTracingWithEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	shutdown, err := SetupTracing(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // nothing to flush; do not wait on the unreachable endpoint
	_ = shutdown(ctx)
}

func TestTraceID(t *testing.T) {
	if got := TraceID(context.Background()); got != "" {
		t.Fatalf("TraceID(empty)=%q", got)
	}
	tid, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	if got := TraceID(ctx); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("TraceID=%q", got)
	}
}

func TestNewLoggerWritesJSON(t *testing.T) {
	buf := &bytes.Buffer{}
	NewLogger(buf, slog.LevelInfo).Info("hello", "k", "v")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil || m["msg"] != "hello" || m["k"] != "v" {
		t.Fatalf("got %q err=%v", buf, err)
	}
}
