package budget

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/mfow/llm-temporal-worker/golang/llm"
	"github.com/mfow/llm-temporal-worker/golang/pricing"
	"github.com/mfow/llm-temporal-worker/golang/routing"
)

type Estimator struct {
	SafetyRatio  *big.Rat
	MaxOutput    int64
	MaxReasoning int64
}

type Estimate struct {
	CandidateID      string
	InputTokens      int64
	OutputTokens     int64
	ReasoningTokens  int64
	CacheWriteTokens int64
	MicroUSD         pricing.MicroUSD
	Currency         string
	CatalogVersion   string
}

func (estimator Estimator) EstimateCandidate(request llm.Request, candidate routing.Candidate, entry pricing.Entry) (Estimate, error) {
	inputTokens, err := estimator.estimateInput(request)
	if err != nil {
		return Estimate{}, err
	}
	outputTokens := estimator.MaxOutput
	if outputTokens <= 0 {
		outputTokens = 1_000
	}
	if request.Output != nil && request.Output.MaxTokens != nil {
		outputTokens = int64(*request.Output.MaxTokens)
	}
	if outputTokens < 0 {
		return Estimate{}, fmt.Errorf("output token limit is negative")
	}
	reasoningTokens := int64(0)
	if request.Reasoning != nil && request.Reasoning.TokenBudget != nil {
		reasoningTokens = int64(*request.Reasoning.TokenBudget)
	}
	if estimator.MaxReasoning > reasoningTokens {
		reasoningTokens = estimator.MaxReasoning
	}
	cacheWrite := inputTokens
	components := []struct {
		price pricing.DecimalUSD
		units int64
		name  string
	}{
		{entry.Prices.InputPerMillion, inputTokens, "input"},
		{entry.Prices.OutputPerMillion, outputTokens, "output"},
		{entry.Prices.ReasoningPerMillion, reasoningTokens, "reasoning"},
		{entry.Prices.CacheWritePerMillion, cacheWrite, "cache_write"},
		{entry.Prices.PerRequest, 1, "per_request"},
	}
	total := pricing.MicroUSD(0)
	for _, component := range components {
		value, err := pricing.CeilMicroUSD(component.price, component.units, 1_000_000)
		if err != nil {
			return Estimate{}, fmt.Errorf("estimate %s: %w", component.name, err)
		}
		total, err = total.Add(value)
		if err != nil {
			return Estimate{}, err
		}
	}
	if estimator.SafetyRatio != nil {
		if estimator.SafetyRatio.Sign() <= 0 {
			return Estimate{}, fmt.Errorf("safety ratio must be positive")
		}
		total, err = multiplyCeil(total, estimator.SafetyRatio)
		if err != nil {
			return Estimate{}, err
		}
	}
	return Estimate{CandidateID: candidate.ID, InputTokens: inputTokens, OutputTokens: outputTokens, ReasoningTokens: reasoningTokens, CacheWriteTokens: cacheWrite, MicroUSD: total, Currency: entry.Currency, CatalogVersion: entry.Version}, nil
}

func (estimator Estimator) EstimatePlan(request llm.Request, plan routing.Plan, entries map[string]pricing.Entry) (Estimate, error) {
	if len(plan.Candidates) == 0 {
		return Estimate{}, fmt.Errorf("cannot estimate an empty route plan")
	}
	var maximum Estimate
	for _, candidate := range plan.Candidates {
		entry, ok := entries[candidate.ID]
		if !ok {
			return Estimate{}, fmt.Errorf("price missing for candidate %s", candidate.ID)
		}
		estimate, err := estimator.EstimateCandidate(request, candidate, entry)
		if err != nil {
			return Estimate{}, err
		}
		if estimate.MicroUSD > maximum.MicroUSD {
			maximum = estimate
		}
	}
	return maximum, nil
}

func (estimator Estimator) estimateInput(request llm.Request) (int64, error) {
	data, err := llm.CanonicalJSON(mustRequestJSON(request))
	if err != nil {
		return 0, err
	}
	// UTF-8 bytes / 4 is a conservative provider-independent baseline for
	// ordinary text. Structural overhead is bounded by the serialized request.
	input := int64((len(data) + 3) / 4)
	if input < 1 {
		input = 1
	}
	if int64(len(data)) > int64(^uint64(0)>>1) {
		return 0, fmt.Errorf("request is too large to estimate")
	}
	return input, nil
}

func multiplyCeil(value pricing.MicroUSD, ratio *big.Rat) (pricing.MicroUSD, error) {
	if value < 0 || ratio == nil {
		return 0, fmt.Errorf("invalid estimate multiplier")
	}
	numerator := new(big.Int).Mul(big.NewInt(int64(value)), ratio.Num())
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, ratio.Denom(), remainder)
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, fmt.Errorf("estimate multiplier overflows int64")
	}
	result := pricing.MicroUSD(quotient.Int64())
	if !result.Valid() {
		return 0, fmt.Errorf("estimate exceeds safe range")
	}
	return result, nil
}

func mustRequestJSON(request llm.Request) []byte {
	data, _ := json.Marshal(request)
	return data
}
