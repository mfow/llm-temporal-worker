package memory

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/state"
)

func TestContinuationStoreBranchesImmutably(t *testing.T) {
	keyring, err := state.NewKeyring([]state.Key{{ID: "k1", Secret: bytes.Repeat([]byte{3}, 32), Primary: true}}, bytes.NewReader(append(bytes.Repeat([]byte{4}, 16), bytes.Repeat([]byte{5}, 16)...)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	store, err := NewContinuationStore(ContinuationOptions{Keyring: keyring, Clock: func() time.Time { return now }, MaxDepth: 4})
	if err != nil {
		t.Fatal(err)
	}
	items := []llm.Item{llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: "hello"}}}}
	_, digest, err := state.CanonicalTranscript(items)
	if err != nil {
		t.Fatal(err)
	}
	root := state.Continuation{Tenant: "tenant", Transcript: items, TranscriptDigest: digest, TranscriptComplete: true, ExpiresAt: now.Add(time.Hour), LastOperationID: "root"}
	rootHandle, err := store.CreateRoot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetForTenant(context.Background(), "tenant", rootHandle)
	if err != nil {
		t.Fatal(err)
	}
	got.Transcript[0] = llm.Message{Actor: llm.ActorHuman}
	again, err := store.Get(context.Background(), rootHandle)
	if err != nil || len(again.Transcript) != 1 {
		t.Fatalf("immutable read failed: %#v %v", again, err)
	}
	child := again
	child.ParentID = rootHandle.String()
	child.Depth = 1
	child.LastOperationID = "op-1"
	child.Transcript = append(child.Transcript, llm.Message{Actor: llm.ActorModel, Content: []llm.Part{llm.TextPart{Text: "world"}}})
	_, child.TranscriptDigest, err = state.CanonicalTranscript(child.Transcript)
	if err != nil {
		t.Fatal(err)
	}
	childHandle, err := store.PutChild(context.Background(), state.PutChildRequest{Parent: rootHandle, Child: child, OperationKey: "op-1"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.PutChild(context.Background(), state.PutChildRequest{Parent: rootHandle, Child: child, OperationKey: "op-1"})
	if err != nil || replay != childHandle {
		t.Fatalf("idempotent child = %q %v", replay, err)
	}
}
