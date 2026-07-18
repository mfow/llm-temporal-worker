package provider

import "context"

type Family string

const (
	FamilyOpenAIResponses   Family = "openai_responses"
	FamilyOpenAIChat        Family = "openai_chat"
	FamilyAnthropicMessages Family = "anthropic_messages"
	FamilyBedrockMessages   Family = "bedrock_messages"
)

func (family Family) Valid() bool {
	switch family {
	case FamilyOpenAIResponses, FamilyOpenAIChat, FamilyAnthropicMessages, FamilyBedrockMessages:
		return true
	default:
		return false
	}
}

// Adapter is the provider boundary. Implementations own one official SDK and
// endpoint family; route selection, retries, budgets, and state remain outside.
type Adapter interface {
	Name() string
	Capabilities(context.Context, CapabilityQuery) (CapabilitySet, error)
	Compile(context.Context, CompileInput) (Call, error)
	Invoke(context.Context, Call, Observer) (Result, error)
}

// EventSource is a one-way source of provider-neutral stream events. It owns
// the provider response body and must stop promptly when either context is
// canceled or Close is called. Once it returns a provider terminal event, its
// next Next call must return io.EOF immediately; emitting another event or
// blocking after a terminal is a provider protocol violation. Every emitted
// ToolArgumentsDelta must contain a nonempty CallID and Name; adapters must
// buffer or normalize provider fragments that discover tool identity later.
type EventSource interface {
	Next(context.Context) (Event, error)
	Close() error
}

// StreamResult records the dispatch observation made while opening a real
// provider stream. Metadata is safe response-header evidence only; event
// payloads continue through EventSource in their original order.
type StreamResult struct {
	Source   EventSource
	Metadata ResponseMetadata
	Dispatch DispatchCertainty
}

// StreamingAdapter is an optional extension of Adapter. It deliberately does
// not provide a default implementation: an Adapter without this port cannot
// be used by Engine.Stream and is rejected before dispatch. Implementations
// must call Observer.BeforePossibleWrite immediately before their first
// possible provider write and return DispatchAccepted only after that call;
// otherwise the engine records the result as ambiguous and refuses replay.
type StreamingAdapter interface {
	Adapter
	OpenStream(context.Context, Call, Observer) (StreamResult, error)
}
