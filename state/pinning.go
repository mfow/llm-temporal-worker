package state

import (
	"fmt"

	"github.com/mfow/llm-temporal-worker/llm"
)

type Compatibility string

const (
	CompatibilityCompatible   Compatibility = "compatible"
	CompatibilityPortable     Compatibility = "portable"
	CompatibilityOptionalDrop Compatibility = "optional_drop"
	CompatibilityRejected     Compatibility = "rejected"
)

type CompatibilityResult struct {
	Decision     Compatibility
	Diagnostics  []llm.Diagnostic
	DroppedState int
}

// CheckPinning determines whether a candidate can consume continuation state.
// Opaque state is only accepted on the exact provider/endpoint/account/family
// lineage. A complete portable transcript permits a best-effort branch when
// all opaque state is explicitly optional.
func CheckPinning(constraints Constraints, candidate Pinning) CompatibilityResult {
	if !constraints.Present {
		return CompatibilityResult{Decision: CompatibilityCompatible}
	}
	sameLineage := equalNonEmpty(constraints.Provider, candidate.Provider) &&
		equalNonEmpty(constraints.EndpointID, candidate.EndpointID) &&
		equalNonEmpty(constraints.AccountRegion, candidate.AccountRegion) &&
		equalNonEmpty(constraints.Family, candidate.Family) &&
		equalNonEmpty(constraints.ModelLineage, candidate.ModelLineage)
	if sameLineage {
		return CompatibilityResult{Decision: CompatibilityCompatible}
	}
	if constraints.RequiresOpaqueState {
		return CompatibilityResult{Decision: CompatibilityRejected, Diagnostics: []llm.Diagnostic{{Code: "continuation_pinned", Severity: llm.DiagnosticError, Message: "provider continuation state is pinned to another lineage"}}}
	}
	if constraints.TranscriptComplete && constraints.Portability == llm.PortabilityBestEffort {
		return CompatibilityResult{Decision: CompatibilityPortable, Diagnostics: []llm.Diagnostic{{Code: "provider_state_dropped", Severity: llm.DiagnosticWarning, Message: "optional provider state was not replayed on this lineage"}}}
	}
	return CompatibilityResult{Decision: CompatibilityRejected, Diagnostics: []llm.Diagnostic{{Code: "continuation_pinned", Severity: llm.DiagnosticError, Message: "continuation requires its original provider lineage"}}}
}

func equalNonEmpty(a, b string) bool {
	return a == "" || b == "" || a == b
}

func ValidatePinning(pin Pinning) error {
	if pin.Provider == "" || pin.EndpointID == "" || pin.Family == "" || pin.ModelLineage == "" {
		return fmt.Errorf("provider pinning requires provider, endpoint, family, and model lineage")
	}
	return nil
}
