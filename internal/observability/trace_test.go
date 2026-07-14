package observability_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mfow/llm-temporal-worker/internal/observability"
	"github.com/mfow/llm-temporal-worker/llm/provider"
	"go.opentelemetry.io/otel/attribute"
)

func TestTracerDropsUnsafeAttributesAndHashesTenant(t *testing.T) {
	exporter := &observability.MemoryExporter{}
	tracer := observability.NewTracer(observability.TraceOptions{Enabled: true, Exporter: exporter})
	ctx, span := tracer.Start(context.Background(), "provider.attempt", attribute.String("tenant", "secret-tenant"), attribute.String("operation_id", "op-1"), attribute.String("prompt", "secret-prompt"))
	span.End()
	if err := tracer.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	spans := exporter.Spans()
	if len(spans) != 1 {
		t.Fatalf("span count = %d", len(spans))
	}
	for _, attr := range spans[0].Attributes() {
		if strings.Contains(attr.Value.AsString(), "secret") || string(attr.Key) == "prompt" {
			t.Fatalf("unsafe trace attribute: %#v", attr)
		}
	}
	foundTenant := false
	for _, attr := range spans[0].Attributes() {
		if string(attr.Key) == "tenant_hash" {
			foundTenant = true
		}
	}
	if !foundTenant {
		t.Fatal("tenant was not hashed")
	}
}

func TestTracerContextUsesBoundTracerAndFlushes(t *testing.T) {
	exporter := &observability.MemoryExporter{}
	tracer := observability.NewTracer(observability.TraceOptions{Enabled: true, Exporter: exporter})
	ctx := observability.WithTracer(context.Background(), tracer)
	if observability.FromContext(ctx) != tracer {
		t.Fatal("context did not retain the configured tracer")
	}
	_, span := observability.FromContext(ctx).Start(ctx, "runtime.reload", attribute.String("outcome", "success"))
	span.End()
	if err := tracer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(exporter.Spans()) != 1 {
		t.Fatalf("flushed span count = %d, want 1", len(exporter.Spans()))
	}
	if err := tracer.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestTracerRecordErrorDropsMalformedProviderFields(t *testing.T) {
	exporter := &observability.MemoryExporter{}
	tracer := observability.NewTracer(observability.TraceOptions{Enabled: true, Exporter: exporter})
	ctx, span := tracer.Start(context.Background(), "provider.attempt")

	secret := "secret-token-value"
	overlong := strings.Repeat("x", 97)
	newline := "invalid\nphase"
	tracer.RecordError(span, provider.NewError(
		provider.Code(secret),
		provider.Phase(newline),
		provider.DispatchCertainty(overlong),
		provider.RetryDisposition(secret),
		"provider failure",
	))
	span.End()
	if err := tracer.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	spans := exporter.Spans()
	if len(spans) != 1 {
		t.Fatalf("span count = %d", len(spans))
	}
	if got := len(spans[0].Attributes()); got != 0 {
		t.Fatalf("malformed provider error exported %d trace attributes, want none", got)
	}
}

func TestTracerRecordErrorKeepsApprovedProviderFields(t *testing.T) {
	exporter := &observability.MemoryExporter{}
	tracer := observability.NewTracer(observability.TraceOptions{Enabled: true, Exporter: exporter})
	ctx, span := tracer.Start(context.Background(), "provider.attempt")
	tracer.RecordError(span, provider.NewError(
		provider.CodeProviderUnavailable,
		provider.PhaseDispatch,
		provider.DispatchRejected,
		provider.RetryNextRoute,
		"provider failure",
	))
	span.End()
	if err := tracer.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	spans := exporter.Spans()
	if len(spans) != 1 {
		t.Fatalf("span count = %d", len(spans))
	}
	if got := len(spans[0].Attributes()); got != 2 {
		t.Fatalf("approved provider error exported %d trace attributes, want 2", got)
	}
	attrs := make(map[string]string, len(spans[0].Attributes()))
	for _, attr := range spans[0].Attributes() {
		attrs[string(attr.Key)] = attr.Value.AsString()
	}
	if got := attrs["error_code"]; got != string(provider.CodeProviderUnavailable) {
		t.Fatalf("error_code = %q, want %q", got, provider.CodeProviderUnavailable)
	}
	if got := attrs["phase"]; got != string(provider.PhaseDispatch) {
		t.Fatalf("phase = %q, want %q", got, provider.PhaseDispatch)
	}
	if _, ok := attrs["dispatch"]; ok {
		t.Fatal("dispatch was exported to trace")
	}
	if _, ok := attrs["retry"]; ok {
		t.Fatal("retry was exported to trace")
	}
}
