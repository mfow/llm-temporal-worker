package postgres

import (
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mfow/llm-temporal-worker/golang/control"
)

func providerControlSnapshot() control.InventorySnapshot {
	model := control.Model{
		ProviderModelID:  "model-a",
		DisplayName:      "Model A",
		OwnedBy:          "provider",
		Lifecycle:        control.LifecycleAvailable,
		CapabilityDigest: sha256.Sum256([]byte("capability")),
		SafeMetadata:     map[string]string{"family": "text"},
	}
	snapshot := control.InventorySnapshot{
		ConfigDigest:        sha256.Sum256([]byte("config")),
		Provider:            "provider",
		EndpointID:          "endpoint",
		EndpointAccountHMAC: sha256.Sum256([]byte("account")),
		EndpointFamily:      "chat",
		Region:              "global",
		Source:              control.InventoryProviderAPI,
		ObservedAt:          time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
		Complete:            false,
		NextCursor:          "cursor-1",
		ExpiresAt:           time.Date(2026, 7, 20, 1, 0, 0, 0, time.UTC),
		Models:              []control.Model{model},
	}
	snapshot.InventoryDigest = control.InventoryDigest(snapshot.Models)
	return snapshot
}

func TestInventoryCursorContextBindsEndpointAndSnapshot(t *testing.T) {
	snapshot := providerControlSnapshot()
	key := []byte("01234567890123456789012345678901")
	repository := InventoryRepository{Keys: Keyring{Active: "inventory-v1", Keys: map[string][]byte{"inventory-v1": key}}}
	recordID := uuid.MustParse("018f7b5d-9ad8-7f5e-8d8c-4cf8e5c4a4d1")
	sealed, err := repository.Keys.Seal(inventoryCursorContext(snapshot, recordID), []byte(snapshot.NextCursor))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := repository.Keys.Open(inventoryCursorContext(snapshot, recordID), sealed)
	if err != nil || string(opened) != snapshot.NextCursor {
		t.Fatalf("open cursor = %q, %v", opened, err)
	}
	changed := snapshot
	changed.EndpointID = "other-endpoint"
	if _, err := repository.Keys.Open(inventoryCursorContext(changed, recordID), sealed); err == nil {
		t.Fatal("cursor opened under a different endpoint")
	}
	changed = snapshot
	changed.InventoryDigest = sha256.Sum256([]byte("different-inventory"))
	if _, err := repository.Keys.Open(inventoryCursorContext(changed, recordID), sealed); err == nil {
		t.Fatal("cursor opened under a different inventory digest")
	}
}

func TestValidateStatusEventRejectsForgedDigest(t *testing.T) {
	now := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	observation := control.StatusObservation{
		ConfigDigest:        sha256.Sum256([]byte("config")),
		RouteID:             "route",
		EndpointID:          "endpoint",
		EndpointAccountHMAC: sha256.Sum256([]byte("account")),
		Provider:            "provider",
		EndpointFamily:      "chat",
		ObservedAt:          now,
		Source:              control.SourceInference,
		Availability:        control.AvailabilityAvailable,
		Credit:              control.CreditOK,
		Billing:             control.BillingOK,
		EvidenceDigest:      sha256.Sum256([]byte("evidence")),
		ConfigEpoch:         "epoch-1",
		ExpiresAt:           now.Add(time.Hour),
	}
	event, err := control.NewStatusEvent(observation)
	if err != nil {
		t.Fatal(err)
	}
	event.EventDigest[0] ^= 0xff
	if err := validateStatusEvent(event); err == nil {
		t.Fatal("forged digest was accepted")
	}
}

func TestProviderControlMigrationStoresIdempotencyAndProjectionDigests(t *testing.T) {
	data, err := schemaFiles.ReadFile("schema/000001_worker_state.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, expected := range []string{
		"event_digest bytea NOT NULL UNIQUE",
		"config_epoch text NOT NULL",
		"last_event_digest bytea NOT NULL",
		"next_cursor_ciphertext bytea",
	} {
		if !strings.Contains(sql, expected) {
			t.Errorf("migration missing %q", expected)
		}
	}
}
