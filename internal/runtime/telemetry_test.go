package runtime

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/config"
	"github.com/mfow/llm-temporal-worker/internal/observability"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestRuntimeConstructsConfiguredTelemetryAndFlushesOnShutdown(t *testing.T) {
	controller := &testWorker{}
	var closed atomic.Bool
	options := testRuntimeOptions(t, controller, &closed)
	exporter := &observability.MemoryExporter{}
	var gotTracing config.TracingConfig
	options.TraceExporterFactory = func(_ context.Context, tracing config.TracingConfig) (sdktrace.SpanExporter, error) {
		gotTracing = tracing
		return exporter, nil
	}
	var logs bytes.Buffer
	options.LogOutput = &logs
	data := []byte(strings.Replace(string(runtimeConfig(t)), "sample_ratio: \"0.05\"", "sample_ratio: \"1\"", 1))
	runtime, err := New(context.Background(), data, options)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Metrics == nil || runtime.Tracer == nil || runtime.Logger == nil {
		t.Fatalf("runtime telemetry = metrics:%v tracer:%v logger:%v", runtime.Metrics != nil, runtime.Tracer != nil, runtime.Logger != nil)
	}
	if gotTracing.OTLPEndpoint == "" || gotTracing.SampleRatio != "1" {
		t.Fatalf("trace exporter configuration = %#v", gotTracing)
	}
	_, span := runtime.Tracer.Start(context.Background(), "llmtw.runtime_test", attribute.String("tenant", "tenant-secret"), attribute.String("prompt", "never-export-this"))
	span.End()
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
	if !closed.Load() {
		t.Fatal("runtime clients were not closed before telemetry shutdown")
	}
	spans := exporter.Spans()
	if len(spans) != 1 {
		t.Fatalf("exported span count = %d, want 1", len(spans))
	}
	for _, attr := range spans[0].Attributes() {
		if string(attr.Key) == "prompt" || strings.Contains(attr.Value.AsString(), "tenant-secret") || strings.Contains(attr.Value.AsString(), "never-export-this") {
			t.Fatalf("trace leaked unsafe runtime value: %#v", attr)
		}
	}
}
