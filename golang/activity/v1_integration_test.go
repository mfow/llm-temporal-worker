package activity

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/mfow/llm-temporal-worker/golang/llm"
	"github.com/mfow/llm-temporal-worker/golang/state"
)

type materializingRuntimeProbe struct {
	events       *[]string
	generateSeen *state.MaterializedState
	compactSeen  *state.MaterializedState
}

func (probe *materializingRuntimeProbe) GenerateV1(_ context.Context, _ llm.GenerateRequestV1) (llm.GenerateResponseV1, error) {
	*probe.events = append(*probe.events, "generate:normal")
	return validMaterializedGenerateResponse("root"), nil
}

func (probe *materializingRuntimeProbe) CompactV1(_ context.Context, _ llm.CompactRequestV1) (llm.CompactResponseV1, error) {
	*probe.events = append(*probe.events, "compact:normal")
	return validMaterializedCompactResponse("compact"), nil
}

func (probe *materializingRuntimeProbe) QueryV1(context.Context, llm.QueryRequestV1) (llm.QueryResponseV1, error) {
	return llm.QueryResponseV1{}, nil
}

func (probe *materializingRuntimeProbe) GenerateV1Materialized(_ context.Context, request llm.GenerateRequestV1, materialized state.MaterializedState) (llm.GenerateResponseV1, error) {
	*probe.events = append(*probe.events, "generate:"+string(materialized.Handle))
	probe.generateSeen = &materialized
	return validMaterializedGenerateResponse(request.OperationKey), nil
}

func (probe *materializingRuntimeProbe) CompactV1Materialized(_ context.Context, request llm.CompactRequestV1, materialized state.MaterializedState) (llm.CompactResponseV1, error) {
	*probe.events = append(*probe.events, "compact:"+string(materialized.Handle))
	probe.compactSeen = &materialized
	return validMaterializedCompactResponse(request.OperationKey), nil
}

type materializingMaterializerProbe struct {
	events    *[]string
	result    state.MaterializedState
	err       error
	gotScope  string
	gotHandle string
}

func (probe *materializingMaterializerProbe) MaterializeHandle(_ context.Context, scopeID, handle string, _ state.MaterializeLimits) (state.MaterializedState, error) {
	probe.gotScope = scopeID
	probe.gotHandle = handle
	*probe.events = append(*probe.events, "materialize:"+scopeID+":"+handle)
	if probe.err != nil {
		return state.MaterializedState{}, probe.err
	}
	return probe.result, nil
}

func validMaterializedState(handle state.Handle) state.MaterializedState {
	return state.MaterializedState{
		Handle:   handle,
		Tenant:   "scope-id",
		Depth:    2,
		Items:    []llm.Item{llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: "replayed"}}}},
		Settings: state.RootModelState("logical-model"),
		Lineage:  []state.Handle{"root", handle},
	}
}

func validMaterializedGenerateResponse(operationKey string) llm.GenerateResponseV1 {
	return llm.GenerateResponseV1{
		APIVersion: llm.APIVersion, OperationKey: operationKey, OperationID: "operation-generate",
		Status:     llm.ResponseStatusCompleted,
		Output:     []llm.Item{llm.Message{Actor: llm.ActorModel, Content: []llm.Part{llm.TextPart{Text: "ok"}}}},
		Checkpoint: llm.CheckpointMetadata{Handle: "checkpoint-generate", Kind: "generation", Depth: 2},
		Cache:      llm.CacheDispositionV1{Disposition: "disabled"},
		Cost:       llm.CostV1{Status: "exact", ActualCostUSD: stringPointer("0"), Method: "provider_reported"},
	}
}

func validMaterializedCompactResponse(operationKey string) llm.CompactResponseV1 {
	parent := llm.CheckpointHandle("parent")
	return llm.CompactResponseV1{
		APIVersion: llm.CompactAPIVersion, OperationKey: operationKey, OperationID: "operation-compact",
		Checkpoint: llm.CheckpointMetadata{Handle: "checkpoint-compact", Parent: &parent, Kind: "compaction", Depth: 3},
		Cache:      llm.CacheDispositionV1{Disposition: "disabled"},
		Cost:       llm.CostV1{Status: "exact", ActualCostUSD: stringPointer("0"), Method: "provider_reported"},
	}
}

func TestMaterializingV1RuntimeMaterializesBeforeGenerateDispatch(t *testing.T) {
	events := []string{}
	runtime := &materializingRuntimeProbe{events: &events}
	materializer := &materializingMaterializerProbe{events: &events, result: validMaterializedState("parent")}
	wrapped := &MaterializingV1Runtime{
		Runtime: runtime, Materializer: materializer,
		Scope: func(requestContext llm.RequestContext) (string, error) {
			return requestContext.Tenant + "/" + requestContext.Project, nil
		},
	}
	parent := llm.CheckpointHandle("parent")
	request := validGenerateV1Request()
	request.Parent = &parent
	response, err := (&Activities{V1Runtime: wrapped}).GenerateV1(context.Background(), request)
	if err != nil {
		t.Fatalf("GenerateV1 error = %v", err)
	}
	if response == nil || response.OperationKey != request.OperationKey {
		t.Fatalf("response = %#v", response)
	}
	if got, want := events, []string{"materialize:tenant/project:parent", "generate:parent"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatch order = %v, want %v", got, want)
	}
	if runtime.generateSeen == nil || runtime.generateSeen.Settings.Model != "logical-model" {
		t.Fatalf("materialized state = %#v, want replayed model", runtime.generateSeen)
	}
}

func TestMaterializingV1RuntimeMaterializesBeforeCompactDispatch(t *testing.T) {
	events := []string{}
	runtime := &materializingRuntimeProbe{events: &events}
	materializer := &materializingMaterializerProbe{events: &events, result: validMaterializedState("parent")}
	wrapped := &MaterializingV1Runtime{
		Runtime: runtime, Materializer: materializer,
		Scope: func(llm.RequestContext) (string, error) { return "scope-id", nil },
	}
	request := validCompactV1Request()
	request.Parent = "parent"
	response, err := (&Activities{V1Runtime: wrapped}).CompactV1(context.Background(), request)
	if err != nil {
		t.Fatalf("CompactV1 error = %v", err)
	}
	if response == nil || response.OperationKey != request.OperationKey {
		t.Fatalf("response = %#v", response)
	}
	if got, want := events, []string{"materialize:scope-id:parent", "compact:parent"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatch order = %v, want %v", got, want)
	}
	if runtime.compactSeen == nil || runtime.compactSeen.Handle != state.Handle("parent") {
		t.Fatalf("materialized state = %#v, want parent", runtime.compactSeen)
	}
}

func TestMaterializingV1RuntimeRootGenerateDoesNotRequireCheckpointMaterializer(t *testing.T) {
	events := []string{}
	runtime := &materializingRuntimeProbe{events: &events}
	wrapped := &MaterializingV1Runtime{Runtime: runtime}
	if _, err := (&Activities{V1Runtime: wrapped}).GenerateV1(context.Background(), validGenerateV1Request()); err != nil {
		t.Fatalf("root GenerateV1 error = %v", err)
	}
	if got, want := events, []string{"generate:normal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root dispatch = %v, want %v", got, want)
	}
}

func TestMaterializingV1RuntimeFailsClosedBeforeDispatchOnMaterializationError(t *testing.T) {
	events := []string{}
	runtime := &materializingRuntimeProbe{events: &events}
	materializer := &materializingMaterializerProbe{events: &events, err: state.ErrNotFound}
	wrapped := &MaterializingV1Runtime{
		Runtime: runtime, Materializer: materializer,
		Scope: func(llm.RequestContext) (string, error) { return "scope-id", nil },
	}
	parent := llm.CheckpointHandle("missing")
	request := validGenerateV1Request()
	request.Parent = &parent
	if _, err := (&Activities{V1Runtime: wrapped}).GenerateV1(context.Background(), request); err == nil {
		t.Fatal("GenerateV1 unexpectedly dispatched after materialization failure")
	}
	if got, want := events, []string{"materialize:scope-id:missing"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestMaterializingV1RuntimeFailsClosedWithoutStateAwareRuntime(t *testing.T) {
	parent := llm.CheckpointHandle("parent")
	request := validGenerateV1Request()
	request.Parent = &parent
	base := &v1RuntimeStub{}
	materializer := &materializingMaterializerProbe{events: new([]string), result: validMaterializedState("parent")}
	wrapped := &MaterializingV1Runtime{
		Runtime: base, Materializer: materializer,
		Scope: func(llm.RequestContext) (string, error) { return "scope-id", nil },
	}
	if _, err := wrapped.GenerateV1(context.Background(), request); err == nil {
		t.Fatal("GenerateV1 unexpectedly accepted a runtime without the state-aware extension")
	}
	if base.generateCalls != 0 {
		t.Fatalf("normal runtime calls = %d, want zero", base.generateCalls)
	}
}

func TestMaterializingV1RuntimeMapsScopeResolverFailure(t *testing.T) {
	parent := llm.CheckpointHandle("parent")
	request := validGenerateV1Request()
	request.Parent = &parent
	wantErr := errors.New("scope lookup failed")
	wrapped := &MaterializingV1Runtime{
		Runtime:      &materializingRuntimeProbe{events: new([]string)},
		Materializer: &materializingMaterializerProbe{events: new([]string), result: validMaterializedState("parent")},
		Scope:        func(llm.RequestContext) (string, error) { return "", wantErr },
	}
	if _, err := wrapped.GenerateV1(context.Background(), request); err == nil {
		t.Fatal("GenerateV1 unexpectedly accepted scope resolver failure")
	}
}
