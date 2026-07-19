// Package compaction contains the provider-independent safeguards used by
// generic conversation compaction.  It deliberately has no engine or storage
// dependency: the engine can persist a policy and reuse the same validation
// before dispatching a durable child operation.
package compaction

import (
	"encoding/json"
	"fmt"
)

const (
	// PolicyVersion is the first version of the generic compaction policy.
	PolicyVersion = "generic-2026-07-18"
	// PromptVersion identifies the repository-owned prompt sent to a generic
	// summarizer.  Prompt changes must change this value so cache keys cannot
	// silently reuse a summary produced by a different prompt.
	PromptVersion = "v1"
)

// SummaryStyle controls how aggressively the generic summarizer compresses
// the selected prefix.  It is a contract value rather than provider-specific
// prompt text.
type SummaryStyle string

const (
	SummaryConcise  SummaryStyle = "concise"
	SummaryBalanced SummaryStyle = "balanced"
	SummaryDetailed SummaryStyle = "detailed"
)

func (style SummaryStyle) valid() bool {
	return style == SummaryConcise || style == SummaryBalanced || style == SummaryDetailed
}

// Policy is the materialized generic compaction policy.  TriggerTokens is a
// caller/engine threshold; TargetTokens is the lower hysteresis target.  The
// remaining fields bound the internal request and prevent an unbounded prompt
// or output from entering a Temporal Activity.
type Policy struct {
	Version                       string       `json:"version"`
	PromptVersion                 string       `json:"prompt_version"`
	SummaryStyle                  SummaryStyle `json:"summary_style"`
	TriggerTokens                 int          `json:"trigger_tokens"`
	TargetTokens                  int          `json:"target_tokens"`
	RecentTurns                   int          `json:"recent_turns"`
	OutputReserveTokens           int          `json:"output_reserve_tokens"`
	MaterializationThresholdBytes int64        `json:"materialization_threshold_bytes"`
}

// DefaultPolicy is intentionally explicit and deterministic.  Callers may
// override it through a validated policy patch, but cannot omit versioning.
func DefaultPolicy() Policy {
	return Policy{
		Version: PolicyVersion, PromptVersion: PromptVersion,
		SummaryStyle: SummaryBalanced, TriggerTokens: 12000, TargetTokens: 8000,
		RecentTurns: 4, OutputReserveTokens: 2000,
		MaterializationThresholdBytes: 4 * 1024 * 1024,
	}
}

// Validate rejects ambiguous or unbounded policies before cache, budget, or
// provider work.  Bounds are deliberately conservative protocol limits, not
// a provider context-window claim.
func (policy Policy) Validate() error {
	if policy.Version != PolicyVersion {
		return fmt.Errorf("unsupported compaction policy version %q", policy.Version)
	}
	if policy.PromptVersion != PromptVersion {
		return fmt.Errorf("unsupported compaction prompt version %q", policy.PromptVersion)
	}
	if !policy.SummaryStyle.valid() {
		return fmt.Errorf("compaction summary_style %q is invalid", policy.SummaryStyle)
	}
	if policy.TriggerTokens < 1 || policy.TriggerTokens > 10_000_000 {
		return fmt.Errorf("compaction trigger_tokens must be between 1 and 10000000")
	}
	if policy.TargetTokens < 1 || policy.TargetTokens >= policy.TriggerTokens {
		return fmt.Errorf("compaction target_tokens must be positive and lower than trigger_tokens")
	}
	if policy.RecentTurns < 0 || policy.RecentTurns > 1000 {
		return fmt.Errorf("compaction recent_turns must be between 0 and 1000")
	}
	if policy.OutputReserveTokens < 1 || policy.OutputReserveTokens > 1_000_000 {
		return fmt.Errorf("compaction output_reserve_tokens must be between 1 and 1000000")
	}
	if policy.MaterializationThresholdBytes < 0 || policy.MaterializationThresholdBytes > 1<<30 {
		return fmt.Errorf("compaction materialization_threshold_bytes is out of bounds")
	}
	return nil
}

// UnmarshalJSON applies defaults for fields omitted by the compact wire
// request, then validates the resulting policy.  This keeps the raw v1 policy
// field backward-compatible while ensuring the engine never receives a
// partially materialized policy.
func (policy *Policy) UnmarshalJSON(data []byte) error {
	defaults := DefaultPolicy()
	type alias Policy
	value := alias(defaults)
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*policy = Policy(value)
	return policy.Validate()
}

func (policy Policy) MarshalJSON() ([]byte, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	type alias Policy
	return json.Marshal(alias(policy))
}
