package engine

import (
	"context"
	"sync"
	"testing"
)

type recordedHeartbeat struct {
	mu       sync.Mutex
	progress []Progress
}

func (heartbeat *recordedHeartbeat) Beat(_ context.Context, progress Progress) error {
	heartbeat.mu.Lock()
	defer heartbeat.mu.Unlock()
	heartbeat.progress = append(heartbeat.progress, progress)
	return nil
}

func (heartbeat *recordedHeartbeat) phases() []string {
	heartbeat.mu.Lock()
	defer heartbeat.mu.Unlock()
	result := make([]string, 0, len(heartbeat.progress))
	for _, progress := range heartbeat.progress {
		result = append(result, progress.Phase)
	}
	return result
}

func TestContextHeartbeatOverridesStaticEngineHeartbeat(t *testing.T) {
	harness := newHarness(t, &fakeAdapter{name: "heartbeat", response: successfulResponse()})
	static := &recordedHeartbeat{}
	bound := &recordedHeartbeat{}
	harness.engine.dependencies.Heartbeat = static

	if _, err := harness.engine.Generate(WithHeartbeat(context.Background(), bound), baseRequest("context-heartbeat")); err != nil {
		t.Fatal(err)
	}
	if got := static.phases(); len(got) != 0 {
		t.Fatalf("static heartbeat received %v, want no progress when an Activity-scoped heartbeat is bound", got)
	}
	got := bound.phases()
	for _, phase := range []string{"planning", "admission", "pre_write", "streaming", "finalization"} {
		if !containsPhase(got, phase) {
			t.Fatalf("bound heartbeat phases = %v, missing %q", got, phase)
		}
	}
}

func containsPhase(phases []string, want string) bool {
	for _, phase := range phases {
		if phase == want {
			return true
		}
	}
	return false
}

var _ Heartbeat = (*recordedHeartbeat)(nil)
