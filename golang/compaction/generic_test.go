package compaction

import (
	"strings"
	"testing"

	"github.com/mfow/llm-temporal-worker/golang/llm"
)

func TestPolicyDefaultsAndValidation(t *testing.T) {
	policy := DefaultPolicy()
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	policy.TargetTokens = policy.TriggerTokens
	if err := policy.Validate(); err == nil {
		t.Fatal("target at trigger threshold unexpectedly accepted")
	}
}

func TestPrepareRequestIsolatesApplicationSettings(t *testing.T) {
	policy := DefaultPolicy()
	max := 100
	request := llm.Request{
		OperationKey: "generate-1", Model: "model-1",
		Tools:      []llm.Tool{{Name: "lookup"}},
		ToolPolicy: llm.ToolPolicy{Mode: llm.ToolChoiceRequired, Name: "lookup"},
		Output:     &llm.OutputSpec{MaxTokens: &max, Format: llm.OutputFormat{Kind: llm.OutputKindJSON}},
		Input:      []llm.Item{llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: "hello"}}}},
	}
	original := request
	compact, err := PrepareRequest(request, "generate-1/compact", request.Input, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(compact.Tools) != 0 || compact.ToolPolicy.Mode != llm.ToolChoiceNone || compact.Continuation != nil {
		t.Fatalf("isolated request retained application controls: %#v", compact)
	}
	if compact.Output == nil || compact.Output.Format.Kind != llm.OutputKindText || compact.Output.MaxTokens == nil || *compact.Output.MaxTokens != policy.OutputReserveTokens {
		t.Fatalf("isolated output = %#v", compact.Output)
	}
	if len(original.Tools) != 1 || original.ToolPolicy.Mode != llm.ToolChoiceRequired || original.Output.Format.Kind != llm.OutputKindJSON {
		t.Fatalf("source request mutated: %#v", original)
	}
}

func TestPlainTextSummaryRejectsToolAndBoundsOutput(t *testing.T) {
	response := llm.Response{Status: llm.ResponseStatusCompleted, Output: []llm.Item{
		llm.Message{Actor: llm.ActorModel, Content: []llm.Part{llm.TextPart{Text: "summary"}}},
	}}
	if got, err := PlainTextSummary(response, 64); err != nil || got != "summary" {
		t.Fatalf("summary = %q, err=%v", got, err)
	}
	response.Output = append(response.Output, llm.ToolCall{ID: "call-1", Name: "lookup", Arguments: []byte(`{}`)})
	if _, err := PlainTextSummary(response, 64); err == nil {
		t.Fatal("tool call accepted as compaction output")
	}
	response.Output = []llm.Item{llm.Message{Actor: llm.ActorModel, Content: []llm.Part{llm.TextPart{Text: strings.Repeat("x", 65)}}}}
	if _, err := PlainTextSummary(response, 64); err == nil {
		t.Fatal("oversized summary accepted")
	}
}

func TestPromptVersionIsPinned(t *testing.T) {
	value, err := Prompt(PromptVersion)
	if err != nil || !strings.Contains(value, "Do not call tools") {
		t.Fatalf("prompt = %q, err=%v", value, err)
	}
	if _, err := Prompt("future"); err == nil {
		t.Fatal("unknown prompt version accepted")
	}
}
