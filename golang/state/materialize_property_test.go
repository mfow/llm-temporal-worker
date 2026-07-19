package state

import (
	"reflect"
	"testing"

	"github.com/mfow/llm-temporal-worker/golang/llm"
)

func message(text string) llm.Item {
	return llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: text}}}
}

func rootCheckpoint(handle, tenant, operation string) Checkpoint {
	return Checkpoint{
		Handle:       Handle(handle),
		Tenant:       tenant,
		Project:      "project",
		OperationKey: operation,
		SettingsPatch: SettingsPatch{
			Model:        SetPatch("gpt-test"),
			ServiceClass: SetPatch(llm.ServiceClassStandard),
		},
		Delta: []llm.Item{message("root")},
	}
}

func childCheckpoint(handle, parent, tenant, operation, text string) Checkpoint {
	parentHandle := Handle(parent)
	return Checkpoint{Handle: Handle(handle), Parent: &parentHandle, Tenant: tenant, OperationKey: operation, Delta: []llm.Item{message(text)}}
}

func TestCheckpointGraphRootLinearAndSiblingMaterialization(t *testing.T) {
	graph := NewCheckpointGraph(MaterializeLimits{})
	if err := graph.PutRoot(rootCheckpoint("root", "tenant-a", "op-root")); err != nil {
		t.Fatal(err)
	}
	if err := graph.PutChild(childCheckpoint("one", "root", "tenant-a", "op-one", "one")); err != nil {
		t.Fatal(err)
	}
	if err := graph.PutChild(childCheckpoint("two", "root", "tenant-a", "op-two", "two")); err != nil {
		t.Fatal(err)
	}
	first, err := graph.Materialize("tenant-a", "one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := graph.Materialize("tenant-a", "two")
	if err != nil {
		t.Fatal(err)
	}
	if first.Depth != 1 || second.Depth != 1 || len(first.Items) != 2 || len(second.Items) != 2 {
		t.Fatalf("unexpected materialized branches: %#v %#v", first, second)
	}
	if first.Items[1].(llm.Message).Content[0].(llm.TextPart).Text != "one" || second.Items[1].(llm.Message).Content[0].(llm.TextPart).Text != "two" {
		t.Fatal("sibling branches shared a delta")
	}
	if _, err := graph.Materialize("tenant-b", "one"); err != ErrTenantMismatch {
		t.Fatalf("cross-tenant materialization error = %v", err)
	}
}

func TestSettingsPatchOmittedSetAndClearRemainDistinct(t *testing.T) {
	base := RootModelState("gpt-test")
	base.Tools = []llm.Tool{{Name: "lookup", InputSchema: []byte(`{"type":"object"}`)}}
	high := llm.ReasoningEffortHigh
	patched, err := ApplySettingsPatch(base, SettingsPatch{ReasoningEffort: SetPatch(high)})
	if err != nil {
		t.Fatal(err)
	}
	if patched.ReasoningEffort != high || len(patched.Tools) != 1 {
		t.Fatalf("omitted leaves were not inherited: %#v", patched)
	}
	cleared, err := ApplySettingsPatch(patched, SettingsPatch{Tools: ClearPatch[[]llm.Tool]()})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Tools != nil || cleared.ReasoningEffort != high {
		t.Fatalf("clear reset unrelated leaves: %#v", cleared)
	}
	if _, err := ApplySettingsPatch(base, SettingsPatch{Model: Patch[string]{Set: ptr("x"), Clear: true}}); err == nil {
		t.Fatal("set and clear were accepted together")
	}
}

func TestMaterializationCarriesEveryAncestorAndSnapshotMatchesReplay(t *testing.T) {
	graph := NewCheckpointGraph(MaterializeLimits{})
	if err := graph.PutRoot(rootCheckpoint("root", "tenant-a", "op-root")); err != nil {
		t.Fatal(err)
	}
	parent := childCheckpoint("one", "root", "tenant-a", "op-one", "one")
	if err := graph.PutChild(parent); err != nil {
		t.Fatal(err)
	}
	if err := graph.PutChild(childCheckpoint("two", "one", "tenant-a", "op-two", "two")); err != nil {
		t.Fatal(err)
	}
	full, err := graph.Materialize("tenant-a", "two")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := NewCheckpointSnapshot(full)
	leaf := childCheckpoint("three", "two", "tenant-a", "op-three", "three")
	snapshot.Items = append(snapshot.Items, message("three"))
	snapshot.Depth = 3
	snapshot.Digest = snapshot.digest()
	leaf.Delta = nil // the self-contained snapshot already includes this node
	leaf.Snapshot = snapshot
	if err := graph.PutChild(leaf); err != nil {
		t.Fatal(err)
	}
	withSnapshot, err := graph.Materialize("tenant-a", "three")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(full.Items, withSnapshot.Items[:len(full.Items)]) || withSnapshot.Depth != 3 {
		t.Fatalf("snapshot replay diverged: full=%#v snapshot=%#v", full, withSnapshot)
	}
	if got := withSnapshot.Items[len(withSnapshot.Items)-1].(llm.Message).Content[0].(llm.TextPart).Text; got != "three" {
		t.Fatalf("snapshot leaf output = %q", got)
	}
}

func TestSnapshotReplayEqualsFullReplay(t *testing.T) {
	withoutSnapshot := NewCheckpointGraph(MaterializeLimits{})
	if err := withoutSnapshot.PutRoot(rootCheckpoint("root", "tenant-a", "op-root")); err != nil {
		t.Fatal(err)
	}
	parent := childCheckpoint("one", "root", "tenant-a", "op-one", "one")
	if err := withoutSnapshot.PutChild(parent); err != nil {
		t.Fatal(err)
	}
	leaf := childCheckpoint("two", "one", "tenant-a", "op-two", "two")
	if err := withoutSnapshot.PutChild(leaf); err != nil {
		t.Fatal(err)
	}
	full, err := withoutSnapshot.Materialize("tenant-a", "two")
	if err != nil {
		t.Fatal(err)
	}

	withSnapshot := NewCheckpointGraph(MaterializeLimits{})
	if err := withSnapshot.PutRoot(rootCheckpoint("root", "tenant-a", "op-root")); err != nil {
		t.Fatal(err)
	}
	if err := withSnapshot.PutChild(parent); err != nil {
		t.Fatal(err)
	}
	snapshotLeaf := leaf
	snapshotLeaf.Snapshot = NewCheckpointSnapshot(full)
	snapshotLeaf.Delta = nil
	if err := withSnapshot.PutChild(snapshotLeaf); err != nil {
		t.Fatal(err)
	}
	optimized, err := withSnapshot.Materialize("tenant-a", "two")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(full.Items, optimized.Items) || !reflect.DeepEqual(full.Settings, optimized.Settings) || full.Depth != optimized.Depth {
		t.Fatalf("snapshot replay diverged: full=%#v optimized=%#v", full, optimized)
	}
}

func TestMaterializationRejectsUnmatchedToolFrontierAndLimits(t *testing.T) {
	graph := NewCheckpointGraph(MaterializeLimits{MaxItems: 2})
	root := rootCheckpoint("root", "tenant-a", "op-root")
	root.Delta = []llm.Item{llm.ToolCall{ID: "call-1", Name: "lookup", Arguments: []byte(`{"q":"x"}`)}}
	if err := graph.PutRoot(root); err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Materialize("tenant-a", "root"); err != nil {
		t.Fatalf("root with pending tool call should be materializable: %v", err)
	}
	bad := childCheckpoint("bad", "root", "tenant-a", "op-bad", "message before result")
	if err := graph.PutChild(bad); err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Materialize("tenant-a", "bad"); err == nil {
		t.Fatal("child began inside an unmatched tool exchange")
	}
	good := childCheckpoint("good", "root", "tenant-a", "op-good", "")
	good.Delta = []llm.Item{llm.ToolResult{CallID: "call-1", Content: []llm.Part{llm.TextPart{Text: "ok"}}}}
	if err := graph.PutChild(good); err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Materialize("tenant-a", "good"); err != nil {
		t.Fatalf("matching tool result rejected: %v", err)
	}
}

func ptr(value string) *string { return &value }
