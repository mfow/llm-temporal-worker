package engine

import (
	"testing"

	"github.com/mfow/llm-temporal-worker/golang/pricing"
)

func TestCompatibilityActualMicroUSDPreservesExactRecoveredCost(t *testing.T) {
	whole := pricing.MustUSD("2.000000000000000001")
	actual, err := compatibilityActualMicroUSD(whole)
	if err != nil {
		t.Fatalf("compatibilityActualMicroUSD() = %v", err)
	}
	if actual != 2_000_001 {
		t.Fatalf("recovered exact cost = %d, want 2000001 (ceil materialization)", actual)
	}

	subMicro := pricing.MustUSD("0.000000000000000001")
	actual, err = compatibilityActualMicroUSD(subMicro)
	if err != nil {
		t.Fatalf("compatibilityActualMicroUSD(sub-micro) = %v", err)
	}
	if actual != 1 {
		t.Fatalf("recovered sub-micro cost = %d, want one compatibility micro-dollar", actual)
	}
}
