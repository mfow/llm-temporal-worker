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
