package routing

import (
	"testing"
	"time"
)

func TestPassiveHealthDoesNotCountAmbiguous(t *testing.T) {
	now := time.Unix(100, 0)
	health := NewPassiveHealth(HealthPolicy{Threshold: 2, Cooldown: time.Minute}, func() time.Time { return now })
	health.Record("r", "v1", FailureAmbiguous)
	if got := health.View("v1").Routes["r"]; got.Open {
		t.Fatal("ambiguous outcome opened route")
	}
	health.Record("r", "v1", FailureDefiniteTransient)
	health.Record("r", "v1", FailureDefiniteTransient)
	if got := health.View("v1").Routes["r"]; !got.Open {
		t.Fatal("threshold did not open route")
	}
	health.Record("r", "v2", FailureSuccess)
	if got := health.View("v2").Routes["r"]; got.Open {
		t.Fatal("snapshot change did not reset health")
	}
}
