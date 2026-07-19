package control

import (
	"context"
	"sync"
	"testing"
	"time"
)

func snapshot(at time.Time) InventorySnapshot {
	return InventorySnapshot{ConfigDigest: digest(1), Provider: "provider", EndpointID: "endpoint", EndpointAccountHMAC: digest(2), EndpointFamily: "chat", Region: "us", Source: InventoryProviderAPI, ObservedAt: at, Complete: true, InventoryDigest: digest(4), ExpiresAt: at.Add(time.Minute), Models: []Model{{ProviderModelID: "model-a", Lifecycle: LifecycleAvailable, CapabilityDigest: digest(5)}}}
}

func TestInventorySnapshotRequiresSortedBoundedModels(t *testing.T) {
	value := snapshot(time.Unix(100, 0))
	value.Models = []Model{{ProviderModelID: "model-b"}, {ProviderModelID: "model-a"}}
	if err := value.Validate(); err == nil {
		t.Fatal("unsorted models were accepted")
	}
	value = snapshot(time.Unix(100, 0))
	value.Source, value.Models = InventoryUnsupported, nil
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := value.ProvenanceAt(time.Unix(101, 0)); got != ProvenanceUnsupported {
		t.Fatalf("unsupported provenance = %q", got)
	}
}

func TestRefreshCoordinatorCollapsesConcurrentCalls(t *testing.T) {
	coordinator := NewRefreshCoordinator()
	var mu sync.Mutex
	calls := 0
	fetch := func(context.Context) (InventorySnapshot, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		return snapshot(time.Unix(100, 0)), nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := coordinator.Refresh(context.Background(), "endpoint", fetch); err != nil {
				t.Errorf("refresh: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", calls)
	}
}

func TestRefreshCoordinatorReturnsStaleSnapshotOnFailure(t *testing.T) {
	coordinator := NewRefreshCoordinator()
	if _, err := coordinator.Refresh(context.Background(), "endpoint", func(context.Context) (InventorySnapshot, error) {
		return snapshot(time.Unix(100, 0)), nil
	}); err != nil {
		t.Fatal(err)
	}
	want := snapshot(time.Unix(100, 0))
	got, err := coordinator.Refresh(context.Background(), "endpoint", func(context.Context) (InventorySnapshot, error) {
		return InventorySnapshot{}, context.DeadlineExceeded
	})
	if err == nil || got.InventoryDigest != want.InventoryDigest {
		t.Fatalf("failed refresh = digest %x, err %v; want stale digest %x and error", got.InventoryDigest, err, want.InventoryDigest)
	}
}

func TestConfiguredModelDoesNotMutateConfiguration(t *testing.T) {
	configured := []string{"configured-model"}
	if !ConfiguredModel(configured, "configured-model") || ConfiguredModel(configured, "discovered-model") {
		t.Fatal("configured model predicate incorrect")
	}
	if len(configured) != 1 {
		t.Fatal("discovery changed configured route list")
	}
}
