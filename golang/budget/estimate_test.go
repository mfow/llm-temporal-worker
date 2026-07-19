package budget

import (
	"math/big"
	"testing"

	"github.com/mfow/llm-temporal-worker/golang/llm"
	"github.com/mfow/llm-temporal-worker/golang/pricing"
	"github.com/mfow/llm-temporal-worker/golang/routing"
)

func TestEstimatePlanUsesMaximumAuthorizedCandidate(t *testing.T) {
	request := llm.Request{OperationKey: "estimate", Model: "logical", Input: []llm.Item{llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: "hello"}}}}, Output: &llm.OutputSpec{MaxTokens: intPointer(10)}}
	plan := routing.Plan{Candidates: []routing.Candidate{{ID: "economy", AttemptedClass: llm.ServiceClassEconomy}, {ID: "priority", AttemptedClass: llm.ServiceClassPriority}}}
	entries := map[string]pricing.Entry{
		"economy":  {Version: "e", Prices: pricing.UnitPrices{OutputPerMillion: pricing.MustDecimalUSD("1")}},
		"priority": {Version: "p", Prices: pricing.UnitPrices{OutputPerMillion: pricing.MustDecimalUSD("2")}},
	}
	estimator := Estimator{SafetyRatio: big.NewRat(1, 1)}
	got, err := estimator.EstimatePlan(request, plan, entries)
	if err != nil {
		t.Fatal(err)
	}
	if got.CandidateID != "priority" {
		t.Fatalf("maximum estimate candidate = %q", got.CandidateID)
	}
}

func TestEstimateCandidateChargesPerRequestInUSD(t *testing.T) {
	request := llm.Request{
		OperationKey: "estimate",
		Model:        "logical",
		Input:        []llm.Item{llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: "hello"}}}},
		Output:       &llm.OutputSpec{MaxTokens: intPointer(1)},
	}
	estimate, err := (Estimator{}).EstimateCandidate(request, routing.Candidate{ID: "candidate"}, pricing.Entry{
		Prices: pricing.UnitPrices{PerRequest: pricing.MustDecimalUSD("0.10")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if estimate.CostUSD.String() != "0.100000000000000000" || estimate.MicroUSD != 100000 {
		t.Fatalf("per-request estimate = %#v", estimate)
	}
}

func TestMatcherContextIncludesCandidateClass(t *testing.T) {
	request := llm.Request{Model: "logical", ServiceClass: llm.ServiceClassStandard}
	context := ContextFor(request, routing.Candidate{EndpointID: "ep", AttemptedClass: llm.ServiceClassPriority}, "prod")
	if context.ServiceClass != llm.ServiceClassPriority || context.EndpointID != "ep" {
		t.Fatalf("unexpected context %#v", context)
	}
}

func intPointer(value int) *int { return &value }
