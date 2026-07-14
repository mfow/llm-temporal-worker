package observability_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mfow/llm-temporal-worker/internal/observability"
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
