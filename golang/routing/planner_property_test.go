package routing

import (
	"bytes"
	"context"
	"reflect"
	"testing"
)

func TestRoutePlanDeterminismInvariants(t *testing.T) {
	catalog := canonicalizationCatalog(t)
	planner := DeterministicPlanner{}
	first, err := planner.Plan(context.Background(), Input{Request: canonicalizationRequest([]string{"alpha", "beta"}), Catalog: catalog})
	if err != nil {
		t.Fatal(err)
	}
	second, err := planner.Plan(context.Background(), Input{Request: canonicalizationRequest([]string{"beta", "alpha"}), Catalog: catalog})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || first.Digest != second.Digest || first.DigestHex != second.DigestHex {
		t.Fatalf("equivalent route plans differ:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if firstJSON, secondJSON := canonicalPlanJSON(t, first), canonicalPlanJSON(t, second); !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("equivalent route plans have distinct canonical bytes:\n%s\n%s", firstJSON, secondJSON)
	}
}
