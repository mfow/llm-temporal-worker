package compaction

import "testing"

func TestPolicyEvaluateTriggerUsesProviderAndPolicyLimits(t *testing.T) {
	policy := DefaultPolicy()
	decision, err := policy.EvaluateTrigger(TriggerInput{
		ProjectedTokens:            9_500,
		ProjectedBytes:             100,
		ProjectedItems:             10,
		ProjectedLineageDepth:      2,
		ProviderContextLimitTokens: 12_000,
		ProviderItemLimit:          100,
		ProviderLineageLimit:       10,
		ReservedReasoningTokens:    500,
		ContinuationExpired:        false,
	})
	if err != nil {
		t.Fatalf("evaluate trigger: %v", err)
	}
	if !decision.ShouldCompact {
		t.Fatal("expected provider context reserve to trigger compaction")
	}
	if decision.Reason != TriggerReasonContextTokens {
		t.Fatalf("reason = %q, want %q", decision.Reason, TriggerReasonContextTokens)
	}
	if decision.TargetTokens != policy.TargetTokens {
		t.Fatalf("target_tokens = %d, want %d", decision.TargetTokens, policy.TargetTokens)
	}
}

func TestPolicyEvaluateTriggerPreservesHysteresisAndReasonPrecedence(t *testing.T) {
	policy := DefaultPolicy()
	decision, err := policy.EvaluateTrigger(TriggerInput{
		ProjectedTokens:       policy.TriggerTokens,
		ProjectedBytes:        policy.MaterializationThresholdBytes,
		ProjectedItems:        100,
		ProviderItemLimit:     100,
		ProjectedLineageDepth: 10,
		ProviderLineageLimit:  10,
		ContinuationExpired:   true,
	})
	if err != nil {
		t.Fatalf("evaluate trigger: %v", err)
	}
	if decision.Reason != TriggerReasonTokens {
		t.Fatalf("reason = %q, want token precedence", decision.Reason)
	}

	decision, err = policy.EvaluateTrigger(TriggerInput{ProjectedTokens: policy.TargetTokens})
	if err != nil {
		t.Fatalf("evaluate below trigger: %v", err)
	}
	if decision.ShouldCompact {
		t.Fatal("target threshold alone must not retrigger compaction")
	}
}

func TestPolicyEvaluateTriggerValidatesInput(t *testing.T) {
	policy := DefaultPolicy()
	for name, input := range map[string]TriggerInput{
		"negative tokens":  {ProjectedTokens: -1},
		"negative bytes":   {ProjectedBytes: -1},
		"negative items":   {ProjectedItems: -1},
		"negative depth":   {ProjectedLineageDepth: -1},
		"negative reserve": {ReservedReasoningTokens: -1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := policy.EvaluateTrigger(input); err == nil {
				t.Fatal("expected invalid input error")
			}
		})
	}
}
