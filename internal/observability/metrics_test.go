package observability_test

import (
	"strings"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/internal/observability"
	"github.com/prometheus/common/expfmt"
)

func TestMetricsBoundLabelsAndNeverExposeTenantText(t *testing.T) {
	metrics, err := observability.NewMetrics(observability.AllowedValues{
		Endpoints: []string{"endpoint-a"}, Models: []string{"model-a"},
		Outcomes: []string{"success"}, Methods: []string{"provider"},
	})
	if err != nil {
		t.Fatal(err)
	}
	metrics.RecordProviderAttempt("endpoint-a", "model-a", "priority", "success", time.Second)
	metrics.RecordProviderAttempt("secret-endpoint", "secret-model", "secret-class", "secret-outcome", time.Second)
	metrics.RecordActivityFailure("worker")
	metrics.RecordActivityFailure("secret-origin")
	metrics.RecordCost("endpoint-a", "model-a", "standard", "provider", 7)
	metrics.RecordCost("endpoint-a", "model-a", "standard", "tenant-secret", 3)

	response := &strings.Builder{}
	metricFamilies, err := metrics.Gather()
	if err != nil {
		t.Fatal(err)
	}
	encoder := expfmt.NewEncoder(response, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, family := range metricFamilies {
		if err := encoder.Encode(family); err != nil {
			t.Fatal(err)
		}
	}
	output := response.String()
	for _, forbidden := range []string{"secret-endpoint", "secret-model", "secret-class", "secret-outcome", "secret-origin", "tenant-secret"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("metrics leaked %q: %s", forbidden, output)
		}
	}
	if !strings.Contains(output, `endpoint="endpoint-a"`) {
		t.Fatalf("configured endpoint was not recorded: %s", output)
	}
	if !strings.Contains(output, `endpoint="other"`) {
		t.Fatalf("unconfigured endpoint was not bounded: %s", output)
	}
	if !strings.Contains(output, `origin="worker"`) || !strings.Contains(output, `origin="other"`) {
		t.Fatalf("failure origins were not restricted to the fixed vocabulary: %s", output)
	}
}
