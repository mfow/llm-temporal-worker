package state

import (
	"testing"

	"github.com/mfow/llm-temporal-worker/golang/llm"
)

func TestCheckPinning(t *testing.T) {
	if got := CheckPinning(Constraints{}, Pinning{}); got.Decision != CompatibilityCompatible {
		t.Fatalf("absent continuation = %#v", got)
	}
	base := Constraints{Present: true, Provider: "anthropic", EndpointID: "prod", Family: "messages", ModelLineage: "claude", TranscriptComplete: true, Portability: llm.PortabilityBestEffort}
	if got := CheckPinning(base, Pinning{Provider: "anthropic", EndpointID: "prod", AccountRegion: "us-east-1", Family: "messages", ModelLineage: "claude"}); got.Decision != CompatibilityCompatible {
		t.Fatalf("same lineage = %#v", got)
	}
	if got := CheckPinning(base, Pinning{Provider: "openai", EndpointID: "prod", Family: "responses", ModelLineage: "gpt"}); got.Decision != CompatibilityPortable {
		t.Fatalf("portable transcript = %#v", got)
	}
	base.RequiresOpaqueState = true
	if got := CheckPinning(base, Pinning{Provider: "openai", EndpointID: "prod", Family: "responses", ModelLineage: "gpt"}); got.Decision != CompatibilityRejected {
		t.Fatalf("opaque mismatch = %#v", got)
	}
	base.RequiresOpaqueState = false
	base.TranscriptComplete = false
	base.Portability = llm.PortabilityStrict
	if got := CheckPinning(base, Pinning{Provider: "openai", EndpointID: "prod", Family: "responses", ModelLineage: "gpt"}); got.Decision != CompatibilityRejected {
		t.Fatalf("incomplete strict transcript = %#v", got)
	}
}
