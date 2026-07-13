package routing

import "fmt"

type Feature string

const (
	FeatureText             Feature = "text"
	FeatureToolCall         Feature = "tool_call"
	FeatureStructuredOutput Feature = "structured_output"
	FeatureReasoning        Feature = "reasoning"
	FeatureContinuation     Feature = "continuation"
)

type CapabilityState string

const (
	CapabilityNative      CapabilityState = "native"
	CapabilityEmulated    CapabilityState = "emulated"
	CapabilityUnsupported CapabilityState = "unsupported"
	CapabilityUnknown     CapabilityState = "unknown"
)

type Capability struct {
	State     CapabilityState
	Transform string
	Reason    string
}

type CapabilitySet struct {
	Version  string
	Features map[Feature]Capability
}

func (state CapabilityState) Valid() bool {
	return state == CapabilityNative || state == CapabilityEmulated || state == CapabilityUnsupported || state == CapabilityUnknown
}

func (set CapabilitySet) Resolve(feature Feature, strict bool) (Capability, error) {
	capability, ok := set.Features[feature]
	if !ok {
		capability = Capability{State: CapabilityUnknown, Reason: "no verified capability"}
	}
	if !capability.State.Valid() {
		return capability, fmt.Errorf("capability %q has invalid state %q", feature, capability.State)
	}
	if strict && (capability.State == CapabilityUnsupported || capability.State == CapabilityUnknown) {
		return capability, fmt.Errorf("capability %q is %s", feature, capability.State)
	}
	return capability, nil
}
