package bedrockmessages

import (
	"errors"
	"testing"

	"github.com/mfow/llm-temporal-worker/llm/provider"
)

func TestMapErrorClassifiesEgressDenialBeforeDispatch(t *testing.T) {
	mapped := mapError(provider.ErrProviderEgressDenied, "bedrock_anthropic")
	if mapped.Code != provider.CodeProviderUnavailable || mapped.Dispatch != provider.DispatchNotDispatched || mapped.Retry != provider.RetryNextRoute {
		t.Fatalf("mapped = %#v", mapped)
	}
	if !errors.Is(mapped, provider.ErrProviderEgressDenied) {
		t.Fatal("mapped error did not preserve the egress marker")
	}
}
