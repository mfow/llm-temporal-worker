package observability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type TraceOptions struct {
	Enabled  bool
	Exporter trace.SpanExporter
}

type Tracer struct {
	provider *trace.TracerProvider
	tracer   oteltrace.Tracer
}

func NewTracer(options TraceOptions) *Tracer {
	providerOptions := make([]trace.TracerProviderOption, 0, 1)
	if options.Enabled && options.Exporter != nil {
		providerOptions = append(providerOptions, trace.WithSpanProcessor(trace.NewSimpleSpanProcessor(options.Exporter)))
	}
	provider := trace.NewTracerProvider(providerOptions...)
	return &Tracer{provider: provider, tracer: provider.Tracer("github.com/mfow/llm-temporal-worker")}
}

func (tracer *Tracer) Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, oteltrace.Span) {
	if tracer == nil || tracer.tracer == nil {
		noopContext, span := oteltrace.NewNoopTracerProvider().Tracer("llmtw").Start(ctx, "event")
		return noopContext, span
	}
	filtered := make([]attribute.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		if safe, ok := safeTraceAttr(attr); ok {
			filtered = append(filtered, safe)
		}
	}
	return tracer.tracer.Start(ctx, safeSpanName(name), oteltrace.WithAttributes(filtered...))
}

func (tracer *Tracer) RecordError(span oteltrace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	attrs := errorAttrs(err)
	for _, attr := range attrs {
		span.SetAttributes(attribute.String(attr.Key, attr.Value.String()))
	}
	span.SetStatus(codes.Error, "operation failed")
}

func (tracer *Tracer) Shutdown(ctx context.Context) error {
	if tracer == nil || tracer.provider == nil {
		return nil
	}
	return tracer.provider.Shutdown(ctx)
}

func safeSpanName(name string) string {
	if name == "" || len(name) > 96 || strings.ContainsAny(name, "\r\n") {
		return "worker.event"
	}
	lower := strings.ToLower(name)
	for _, word := range unsafeMessageWords {
		if strings.Contains(lower, word) {
			return "worker.event"
		}
	}
	return name
}

func safeTraceAttr(attr attribute.KeyValue) (attribute.KeyValue, bool) {
	key := string(attr.Key)
	if key == "tenant" {
		return attribute.String("tenant_hash", hashTenant(attr.Value.AsString())), true
	}
	switch key {
	case "trace_id", "span_id", "workflow_id", "run_id", "activity_id", "operation_id", "route_id", "tenant_hash", "endpoint", "model", "service_class", "phase", "error_code", "status", "outcome":
	default:
		return attribute.KeyValue{}, false
	}
	if attr.Value.Type() != attribute.STRING {
		return attribute.KeyValue{}, false
	}
	value := attr.Value.AsString()
	if value == "" || len(value) > 96 || strings.ContainsAny(value, "\r\n") {
		return attribute.KeyValue{}, false
	}
	return attr, true
}

func hashTenant(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:8])
}

// MemoryExporter is a bounded test/export hook. Production deployments can
// supply an OTLP exporter without changing worker lifecycle code.
type MemoryExporter struct {
	mu    sync.Mutex
	spans []trace.ReadOnlySpan
}

func (exporter *MemoryExporter) ExportSpans(_ context.Context, spans []trace.ReadOnlySpan) error {
	if exporter == nil {
		return nil
	}
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	exporter.spans = append(exporter.spans, spans...)
	return nil
}

func (*MemoryExporter) Shutdown(context.Context) error { return nil }

func (exporter *MemoryExporter) Spans() []trace.ReadOnlySpan {
	if exporter == nil {
		return nil
	}
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	return append([]trace.ReadOnlySpan(nil), exporter.spans...)
}

var _ trace.SpanExporter = (*MemoryExporter)(nil)
