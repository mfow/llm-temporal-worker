package postgres

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/control"
)

func TestInventoryModelPageReadsLatestSnapshotWithPinnedKeyset(t *testing.T) {
	ctx, namespace, pool, cleanup := providerControlIntegrationPool(t)
	defer cleanup()

	configDigest := sha256.Sum256([]byte("inventory-model-query-" + time.Now().UTC().Format(time.RFC3339Nano)))
	configs, err := namespace.Render("configuration_snapshots")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO "+configs+" (config_digest, config_version, source_digest, sanitized_config) VALUES ($1,$2,$1,'{}'::jsonb)", configDigest[:], "inventory-model-query"); err != nil {
		t.Fatal(err)
	}

	observed := time.Now().UTC().Truncate(time.Microsecond)
	repository := DefaultInventoryRepository(pool, namespace, Keyring{Active: "inventory-v1", Keys: map[string][]byte{"inventory-v1": []byte("01234567890123456789012345678901")}})
	old := inventoryQuerySnapshot(configDigest, "provider-a", "endpoint-a", observed, "old-model")
	if _, err := repository.PersistSnapshot(ctx, old); err != nil {
		t.Fatal(err)
	}
	latestA := inventoryQuerySnapshot(configDigest, "provider-a", "endpoint-a", observed.Add(time.Minute), "model-a")
	latestA.Models = append(latestA.Models, inventoryQueryModel("model-b", control.LifecycleDeprecated))
	latestA.InventoryDigest = control.InventoryDigest(latestA.Models)
	if _, err := repository.PersistSnapshot(ctx, latestA); err != nil {
		t.Fatal(err)
	}
	latestB := inventoryQuerySnapshot(configDigest, "provider-a", "endpoint-b", observed.Add(2*time.Minute), "model-c")
	latestB.Models = append(latestB.Models, inventoryQueryModel("model-d", control.LifecycleUnavailable))
	latestB.InventoryDigest = control.InventoryDigest(latestB.Models)
	if _, err := repository.PersistSnapshot(ctx, latestB); err != nil {
		t.Fatal(err)
	}

	first, err := repository.ListInventoryModels(ctx, InventoryModelListOptions{ConfigDigest: configDigest, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if first.SnapshotHorizon.Equal(time.Time{}) || first.SnapshotHorizon.Before(latestB.ObservedAt) {
		t.Fatalf("snapshot horizon = %s, want at least %s", first.SnapshotHorizon, latestB.ObservedAt)
	}
	if len(first.Models) != 2 || first.Models[0].Model.ProviderModelID != "model-a" || first.Models[1].Model.ProviderModelID != "model-b" {
		t.Fatalf("first page models = %#v", first.Models)
	}
	if first.Models[0].Snapshot.ID != first.Models[1].Snapshot.ID || first.Models[0].Snapshot.ObservedAt != latestA.ObservedAt {
		t.Fatalf("first page did not use one latest endpoint snapshot: %#v", first.Models)
	}
	if first.Next == nil || first.Next.ProviderModelID != "model-b" || first.Next.EndpointID != "endpoint-a" {
		t.Fatalf("first next position = %#v", first.Next)
	}
	if first.Models[0].Snapshot.ProvenanceAt(latestA.ObservedAt) != control.ProvenanceCurrent {
		t.Fatal("current snapshot was not reported current")
	}

	second, err := repository.ListInventoryModels(ctx, InventoryModelListOptions{
		ConfigDigest: configDigest, SnapshotHorizon: first.SnapshotHorizon, After: *first.Next, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Models) != 2 || second.Models[0].Model.ProviderModelID != "model-c" || second.Models[1].Model.ProviderModelID != "model-d" || second.Next != nil {
		t.Fatalf("second page models = %#v, next=%#v", second.Models, second.Next)
	}
	for _, model := range append(first.Models, second.Models...) {
		if model.Model.ProviderModelID == "old-model" {
			t.Fatal("stale endpoint snapshot leaked into latest page")
		}
	}

	filtered, err := repository.ListInventoryModels(ctx, InventoryModelListOptions{ConfigDigest: configDigest, EndpointID: "endpoint-a", ModelPrefix: "model-", Lifecycle: control.LifecycleDeprecated})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Models) != 1 || filtered.Models[0].Model.ProviderModelID != "model-b" {
		t.Fatalf("filtered page = %#v", filtered.Models)
	}
}

func TestInventoryModelPageReportsEmptyUnsupportedSnapshotHorizon(t *testing.T) {
	ctx, namespace, pool, cleanup := providerControlIntegrationPool(t)
	defer cleanup()

	configDigest := sha256.Sum256([]byte("inventory-model-unsupported-" + time.Now().UTC().Format(time.RFC3339Nano)))
	configs, err := namespace.Render("configuration_snapshots")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO "+configs+" (config_digest, config_version, source_digest, sanitized_config) VALUES ($1,$2,$1,'{}'::jsonb)", configDigest[:], "inventory-model-unsupported"); err != nil {
		t.Fatal(err)
	}
	observed := time.Now().UTC().Truncate(time.Microsecond)
	snapshot := inventoryQuerySnapshot(configDigest, "provider-b", "endpoint-unsupported", observed, "")
	snapshot.Source = control.InventoryUnsupported
	snapshot.Models = nil
	snapshot.Complete = true
	snapshot.NextCursor = ""
	snapshot.InventoryDigest = control.InventoryDigest(nil)
	repository := DefaultInventoryRepository(pool, namespace, Keyring{Active: "inventory-v1", Keys: map[string][]byte{"inventory-v1": []byte("01234567890123456789012345678901")}})
	if _, err := repository.PersistSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	page, err := repository.ListInventoryModels(ctx, InventoryModelListOptions{ConfigDigest: configDigest, Provider: "provider-b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Models) != 0 || page.Next != nil || page.SnapshotHorizon.IsZero() {
		t.Fatalf("unsupported page = %#v", page)
	}
}

func TestInventoryModelPageKeepsEndpointAccountEpochsDistinct(t *testing.T) {
	ctx, namespace, pool, cleanup := providerControlIntegrationPool(t)
	defer cleanup()

	configDigest := sha256.Sum256([]byte("inventory-model-account-" + time.Now().UTC().Format(time.RFC3339Nano)))
	configs, err := namespace.Render("configuration_snapshots")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO "+configs+" (config_digest, config_version, source_digest, sanitized_config) VALUES ($1,$2,$1,'{}'::jsonb)", configDigest[:], "inventory-model-account"); err != nil {
		t.Fatal(err)
	}
	observed := time.Now().UTC().Truncate(time.Microsecond)
	first := inventoryQuerySnapshot(configDigest, "provider-c", "endpoint-c", observed, "account-one-model")
	second := inventoryQuerySnapshot(configDigest, "provider-c", "endpoint-c", observed.Add(time.Minute), "account-two-model")
	second.EndpointAccountHMAC = sha256.Sum256([]byte("endpoint-c-account-rotated"))
	repository := DefaultInventoryRepository(pool, namespace, Keyring{Active: "inventory-v1", Keys: map[string][]byte{"inventory-v1": []byte("01234567890123456789012345678901")}})
	if _, err := repository.PersistSnapshot(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PersistSnapshot(ctx, second); err != nil {
		t.Fatal(err)
	}
	page, err := repository.ListInventoryModels(ctx, InventoryModelListOptions{ConfigDigest: configDigest, EndpointID: "endpoint-c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Models) != 2 {
		t.Fatalf("account-epoch page = %#v", page.Models)
	}
	seen := map[string]bool{}
	for _, row := range page.Models {
		seen[row.Model.ProviderModelID] = true
	}
	if !seen["account-one-model"] || !seen["account-two-model"] {
		t.Fatalf("account epochs collapsed or lost: %#v", seen)
	}
}

func inventoryQuerySnapshot(configDigest [32]byte, provider, endpoint string, observed time.Time, modelID string) control.InventorySnapshot {
	models := []control.Model{}
	if modelID != "" {
		models = append(models, inventoryQueryModel(modelID, control.LifecycleAvailable))
	}
	snapshot := control.InventorySnapshot{
		ConfigDigest:        configDigest,
		Provider:            provider,
		EndpointID:          endpoint,
		EndpointAccountHMAC: sha256.Sum256([]byte(endpoint + "-account")),
		EndpointFamily:      "chat",
		Region:              "global",
		Source:              control.InventoryProviderAPI,
		ObservedAt:          observed,
		Complete:            true,
		ExpiresAt:           observed.Add(time.Hour),
		Models:              models,
	}
	snapshot.InventoryDigest = control.InventoryDigest(snapshot.Models)
	return snapshot
}

func inventoryQueryModel(id string, lifecycle control.Lifecycle) control.Model {
	return control.Model{
		ProviderModelID:  id,
		DisplayName:      id,
		OwnedBy:          "provider",
		Lifecycle:        lifecycle,
		CapabilityDigest: sha256.Sum256([]byte("capability-" + id)),
		SafeMetadata:     map[string]string{"family": "text"},
	}
}
