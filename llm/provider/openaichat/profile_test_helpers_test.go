package openaichat

import "github.com/mfow/llm-temporal-worker/llm/provider"

func profileTestCapabilities(version string) provider.CapabilitySet {
	features := make(map[provider.Feature]provider.Capability, len(allFeatures()))
	for _, feature := range allFeatures() {
		state := provider.CapabilityNative
		if feature == provider.FeatureDocument || feature == provider.FeatureContinuation || feature == provider.FeatureStreaming {
			state = provider.CapabilityUnsupported
		}
		features[feature] = provider.Capability{State: state, Reason: "profile fixture"}
	}
	return provider.CapabilitySet{Version: version, Features: features}
}
