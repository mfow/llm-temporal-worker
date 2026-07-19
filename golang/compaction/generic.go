package compaction

import (
	"embed"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mfow/llm-temporal-worker/golang/llm"
)

//go:embed prompt/v1.txt
var promptFiles embed.FS

// Prompt returns the immutable repository-owned generic compaction prompt.
func Prompt(version string) (string, error) {
	if version != PromptVersion {
		return "", fmt.Errorf("unsupported generic compaction prompt version %q", version)
	}
	value, err := promptFiles.ReadFile("prompt/v1.txt")
	if err != nil {
		return "", fmt.Errorf("read generic compaction prompt: %w", err)
	}
	return string(value), nil
}

// PrepareRequest constructs the isolated summarizer call.  It copies the
// caller's routing and sampling settings, but never mutates the caller and
// always strips application tools, tool policy, continuation, and structured
// output.  The returned request can therefore only ask for bounded plain text.
func PrepareRequest(source llm.Request, operationKey string, input []llm.Item, policy Policy) (llm.Request, error) {
	if operationKey == "" {
		return llm.Request{}, errors.New("compaction operation key is required")
	}
	if err := policy.Validate(); err != nil {
		return llm.Request{}, err
	}
	if len(input) == 0 {
		return llm.Request{}, errors.New("compaction input must not be empty")
	}
	maxTokens := policy.OutputReserveTokens
	result := source
	result.OperationKey = operationKey
	result.Input = append([]llm.Item(nil), input...)
	result.Instructions = append([]llm.Instruction(nil), source.Instructions...)
	result.Tools = nil
	result.ToolPolicy = llm.ToolPolicy{Mode: llm.ToolChoiceNone}
	result.Output = &llm.OutputSpec{MaxTokens: &maxTokens, Format: llm.OutputFormat{Kind: llm.OutputKindText}}
	result.Continuation = nil
	if source.ServiceClassFallbacks != nil {
		result.ServiceClassFallbacks = append([]llm.ServiceClass(nil), source.ServiceClassFallbacks...)
	}
	if source.Sampling != nil {
		value := *source.Sampling
		value.StopSequences = append([]string(nil), source.Sampling.StopSequences...)
		result.Sampling = &value
	}
	if source.Reasoning != nil {
		value := *source.Reasoning
		result.Reasoning = &value
	}
	return result, nil
}

// PlainTextSummary extracts only a completed model message containing text
// parts.  Tool calls, tool results, provider state, multimodal parts, and
// non-completed statuses are rejected before a summary can become state.
func PlainTextSummary(response llm.Response, maxBytes int) (string, error) {
	if response.Status != llm.ResponseStatusCompleted {
		return "", fmt.Errorf("compaction response status %q is not completed", response.Status)
	}
	if maxBytes <= 0 {
		return "", errors.New("compaction summary byte limit must be positive")
	}
	var builder strings.Builder
	for _, item := range response.Output {
		message, ok := item.(llm.Message)
		if !ok || message.Actor != llm.ActorModel {
			return "", fmt.Errorf("compaction output contains non-model message")
		}
		for _, part := range message.Content {
			textPart, ok := part.(llm.TextPart)
			if !ok {
				return "", fmt.Errorf("compaction output contains non-text content")
			}
			if !utf8.ValidString(textPart.Text) {
				return "", errors.New("compaction output is not valid UTF-8")
			}
			builder.WriteString(textPart.Text)
		}
	}
	if builder.Len() == 0 {
		return "", errors.New("compaction output is empty")
	}
	if builder.Len() > maxBytes {
		return "", fmt.Errorf("compaction output is %d bytes; limit is %d", builder.Len(), maxBytes)
	}
	return builder.String(), nil
}
