package postgres

import (
	"context"
	"crypto/sha256"
	"os"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/control"
)

func TestProviderControlPersistenceIntegration(t *testing.T) {
	addr := os.Getenv("LLMTW_POSTGRES_ADDR")
	if addr == "" {
		t.Skip("LLMTW_POSTGRES_ADDR is not configured; set it for PostgreSQL integration tests")
	}
	namespace, err := NewNamespace(valueOr("LLMTW_POSTGRES_DATABASE", "llm_worker"), valueOr("LLMTW_POSTGRES_SCHEMA", "llm_worker"), os.Getenv("LLMTW_POSTGRES_TABLE_PREFIX"))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := NewPool(context.Background(), PoolOptions{
		Namespace:        namespace,
		Addresses:        []string{addr},
		Username:         valueOr("LLMTW_POSTGRES_USER", "llmtw"),
		Password:         valueOr("LLMTW_POSTGRES_PASSWORD", "llmtw"),
		MaxConnections:   4,
		MinConnections:   1,
		DialTimeout:      5 * time.Second,
		StatementTimeout: 5 * time.Second,
		LockTimeout:      time.Second,
		IdleTxTimeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Install(ctx, pool, namespace); err != nil {
		t.Fatalf("install schema: %v", err)
	}

	configDigest := sha256.Sum256([]byte("provider-control-integration-config-" + time.Now().UTC().Format(time.RFC3339Nano)))
	configs, err := namespace.Render("configuration_snapshots")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO "+configs+" (config_digest, config_version, source_digest, sanitized_config) VALUES ($1,$2,$1,'{}'::jsonb)", configDigest[:], "provider-control-integration"); err != nil {
		t.Fatal(err)
	}

	observed := time.Now().UTC().Truncate(time.Microsecond)
	observation := control.StatusObservation{
		ConfigDigest:        configDigest,
		RouteID:             "provider-control-route",
		EndpointID:          "provider-control-endpoint",
		EndpointAccountHMAC: sha256.Sum256([]byte("provider-control-account")),
		Provider:            "provider-control-provider",
		EndpointFamily:      "chat",
		ObservedAt:          observed,
		Source:              control.SourceInference,
		Availability:        control.AvailabilityAvailable,
		Credit:              control.CreditOK,
		Billing:             control.BillingOK,
		EvidenceDigest:      sha256.Sum256([]byte("provider-control-evidence")),
		ConfigEpoch:         "provider-control-epoch-1",
		ExpiresAt:           observed.Add(time.Hour),
	}
	event, err := control.NewStatusEvent(observation)
	if err != nil {
		t.Fatal(err)
	}
	statusRepository := DefaultProviderStatusRepository(pool, namespace)
	applied, err := statusRepository.PersistStatusEvent(ctx, event)
	if err != nil || !applied {
		t.Fatalf("persist initial status: applied=%v err=%v", applied, err)
	}
	if duplicate, err := statusRepository.PersistStatusEvent(ctx, event); err != nil || duplicate {
		t.Fatalf("persist duplicate status: applied=%v err=%v", duplicate, err)
	}
	status, err := statusRepository.GetRouteStatus(ctx, configDigest, observation.RouteID)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastEventDigest != event.EventDigest || status.Availability != control.AvailabilityAvailable || status.ConsecutiveDefiniteFailures != 0 {
		t.Fatalf("initial route projection = %#v", status)
	}

	inventory := providerControlSnapshot()
	inventory.ConfigDigest = configDigest
	inventory.EndpointID = observation.EndpointID
	inventory.EndpointAccountHMAC = observation.EndpointAccountHMAC
	inventory.ObservedAt = observed.Add(time.Minute)
	inventory.ExpiresAt = inventory.ObservedAt.Add(time.Hour)
	inventoryRepository := DefaultInventoryRepository(pool, namespace, Keyring{Active: "inventory-v1", Keys: map[string][]byte{"inventory-v1": []byte("01234567890123456789012345678901")}})
	record, err := inventoryRepository.PersistSnapshot(ctx, inventory)
	if err != nil {
		t.Fatalf("persist inventory: %v", err)
	}
	if cursor, err := inventoryRepository.OpenCursor(record); err != nil || cursor != inventory.NextCursor {
		t.Fatalf("open persisted inventory cursor = %q, %v", cursor, err)
	}
	reloaded, err := inventoryRepository.GetSnapshot(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Snapshot.InventoryDigest != inventory.InventoryDigest || len(reloaded.Snapshot.Models) != 1 {
		t.Fatalf("reloaded inventory = %#v", reloaded.Snapshot)
	}
	if cursor, err := inventoryRepository.OpenCursor(reloaded); err != nil || cursor != inventory.NextCursor {
		t.Fatalf("open reloaded inventory cursor = %q, %v", cursor, err)
	}
}
