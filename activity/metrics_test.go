package activity

import (
	"context"
	"testing"

	"github.com/mfow/llm-temporal-worker/internal/observability"
	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
)

func TestGenerateActivityRecordsTerminalMetrics(t *testing.T) {
	tests := []struct {
		name       string
		engineErr  error
		status     string
		errorClass string
	}{
		{name: "completed", status: "completed", errorClass: "none"},
		{name: "provider transient", engineErr: provider.NewError(provider.CodeProviderUnavailable, provider.PhaseDispatch, provider.DispatchRejected, provider.RetryNextRoute, "unavailable"), status: "failed", errorClass: "provider_unavailable"},
		{name: "canceled", engineErr: provider.NewError(provider.CodeCanceled, provider.PhaseDispatch, provider.DispatchNotDispatched, provider.RetryNever, "canceled"), status: "canceled", errorClass: "none"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics := newActivityTestMetrics(t)
			activities := Activities{Metrics: metrics, Engine: &fakeEngine{err: test.engineErr, response: llm.Response{OperationKey: "operation-1", OperationID: "operation-id", Status: llm.ResponseStatusCompleted, Service: llm.ServiceFacts{Requested: llm.ServiceClassStandard, Attempted: llm.ServiceClassStandard}}}}
			_, _ = activities.Generate(context.Background(), validGeneratePayload())
			assertActivityMetricCounter(t, metrics, "llmtw_activity_total", map[string]string{"status": test.status, "error_class": test.errorClass}, 1)
			assertActivityMetricHistogramCount(t, metrics, "llmtw_activity_duration_seconds", map[string]string{"phase": "total"}, 1)
		})
	}
}

func validGeneratePayload() GenerateRequest {
	return GenerateRequest{APIVersion: APIVersion, Request: llm.Request{OperationKey: "operation-1", Model: "model-1", Input: []llm.Item{llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: "hello"}}}}}}
}

func newActivityTestMetrics(t *testing.T) *observability.Metrics {
	t.Helper()
	metrics, err := observability.NewMetrics(observability.AllowedValues{Statuses: []string{"completed", "failed", "canceled"}, ErrorClasses: []string{"none", "provider_unavailable", "budget_denied", "internal"}, Phases: []string{"total"}})
	if err != nil {
		t.Fatal(err)
	}
	return metrics
}

func assertActivityMetricCounter(t *testing.T, metrics *observability.Metrics, name string, labels map[string]string, want float64) {
	t.Helper()
	got, found := activityMetricValue(t, metrics, name, labels, false)
	if !found || got != want {
		t.Fatalf("%s%v = %v, found=%v; want %v", name, labels, got, found, want)
	}
}

func assertActivityMetricHistogramCount(t *testing.T, metrics *observability.Metrics, name string, labels map[string]string, want uint64) {
	t.Helper()
	got, found := activityMetricValue(t, metrics, name, labels, true)
	if !found || uint64(got) != want {
		t.Fatalf("%s%v count = %v, found=%v; want %v", name, labels, got, found, want)
	}
}

func activityMetricValue(t *testing.T, metrics *observability.Metrics, name string, labels map[string]string, histogram bool) (float64, bool) {
	t.Helper()
	families, err := metrics.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			matches := len(metric.Label) == len(labels)
			for _, label := range metric.Label {
				if labels[label.GetName()] != label.GetValue() {
					matches = false
					break
				}
			}
			if matches {
				if histogram {
					return float64(metric.GetHistogram().GetSampleCount()), true
				}
				return metric.GetCounter().GetValue(), true
			}
		}
	}
	return 0, false
}
