package provider_test

import (
	"strings"
	"testing"

	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
)

func FuzzAssemblerEventSequences(f *testing.F) {
	f.Add("semantic stream")
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			return
		}
		text := strings.ToValidUTF8(raw, "?")
		assembler := provider.NewAssembler("assembler-fuzz")
		for _, event := range []provider.Event{
			provider.OutputStarted{Index: 0},
			provider.TextDelta{Index: 0, Text: text},
			provider.OutputFinished{Index: 0},
			provider.StreamCompleted{},
		} {
			if err := assembler.Add(event); err != nil {
				t.Fatalf("Add(%T): %v", event, err)
			}
		}
		response, err := assembler.Result()
		if err != nil {
			t.Fatal(err)
		}
		if response.OperationKey != "assembler-fuzz" || response.Status != llm.ResponseStatusCompleted || len(response.Output) != 1 {
			t.Fatalf("assembled response = %#v", response)
		}
		message, ok := response.Output[0].(llm.Message)
		if !ok {
			t.Fatalf("assembled output = %#v", response.Output)
		}
		if text == "" {
			if len(message.Content) != 0 {
				t.Fatalf("empty assembled text produced content = %#v", message.Content)
			}
			return
		}
		if len(message.Content) != 1 {
			t.Fatalf("assembled output = %#v", response.Output)
		}
		part, ok := message.Content[0].(llm.TextPart)
		if !ok || part.Text != text {
			t.Fatalf("assembled text = %#v, want %q", message.Content, text)
		}
	})
}
