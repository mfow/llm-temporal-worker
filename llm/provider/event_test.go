package provider_test

import (
	"testing"

	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
)

func TestAssemblerBuildsTerminalResponseInOrder(t *testing.T) {
	assembler := provider.NewAssembler("operation-1")
	message := llm.Message{Actor: llm.ActorModel, Content: []llm.Part{llm.TextPart{Text: "hello"}}}
	for _, event := range []provider.Event{
		provider.OutputStarted{Index: 0},
		provider.TextDelta{Index: 0, Text: "he"},
		provider.TextDelta{Index: 0, Text: "llo"},
		provider.OutputFinished{Index: 0, Item: message},
		provider.UsageUpdated{Usage: llm.Usage{InputTokens: 2, OutputTokens: 1}},
		provider.StreamCompleted{},
	} {
		if err := assembler.Add(event); err != nil {
			t.Fatalf("add %T: %v", event, err)
		}
	}
	response, err := assembler.Result()
	if err != nil {
		t.Fatal(err)
	}
	if response.OperationKey != "operation-1" || response.Status != llm.ResponseStatusCompleted {
		t.Fatalf("response identity = %#v", response)
	}
	if len(response.Output) != 1 || response.Usage.InputTokens != 2 {
		t.Fatalf("assembled response = %#v", response)
	}
}

func TestAssemblerRejectsInvalidOrderAndTerminalReuse(t *testing.T) {
	for _, events := range [][]provider.Event{
		{provider.TextDelta{Index: 0, Text: "before start"}},
		{provider.OutputStarted{Index: 0}, provider.OutputStarted{Index: 0}},
		{provider.OutputStarted{Index: 0}, provider.StreamCompleted{}},
		{provider.OutputStarted{Index: 0}, provider.OutputFinished{Index: 0}, provider.StreamCompleted{}, provider.UsageUpdated{}},
	} {
		assembler := provider.NewAssembler("operation-1")
		failed := false
		for _, event := range events {
			if err := assembler.Add(event); err != nil {
				failed = true
				break
			}
		}
		if !failed {
			t.Errorf("accepted invalid event sequence %#v", events)
		}
	}
	if _, err := provider.NewAssembler("operation-1").Result(); err == nil {
		t.Fatal("returned a result before a terminal event")
	}
}

func TestAssemblerRejectsInvalidUTF8AndUnfinishedOutput(t *testing.T) {
	assembler := provider.NewAssembler("operation-1")
	if err := assembler.Add(provider.OutputStarted{Index: 0}); err != nil {
		t.Fatal(err)
	}
	if err := assembler.Add(provider.TextDelta{Index: 0, Text: string([]byte{0xff})}); err == nil {
		t.Fatal("accepted invalid UTF-8 delta")
	}
	if err := assembler.Add(provider.StreamCompleted{}); err == nil {
		t.Fatal("accepted terminal event with unfinished output")
	}
}
