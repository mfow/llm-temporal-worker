package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mfow/llm-temporal-worker/golang/control"
	"github.com/mfow/llm-temporal-worker/golang/maintenance"
)

func maintenanceIntegrationRepository(t *testing.T) (MaintenanceRepository, context.Context, func()) {
	t.Helper()
	if os.Getenv("LLMTW_POSTGRES_ADDR") == "" {
		t.Skip("LLMTW_POSTGRES_ADDR is not configured; set it for PostgreSQL maintenance tests")
	}
	ns, err := NewNamespace(valueOr("LLMTW_POSTGRES_DATABASE", "llm_worker"), valueOr("LLMTW_POSTGRES_SCHEMA", "llm_worker"), os.Getenv("LLMTW_POSTGRES_TABLE_PREFIX"))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := NewPool(context.Background(), PoolOptions{Namespace: ns, Addresses: []string{os.Getenv("LLMTW_POSTGRES_ADDR")}, Username: valueOr("LLMTW_POSTGRES_USER", "llmtw"), Password: valueOr("LLMTW_POSTGRES_PASSWORD", "llmtw"), MaxConnections: 4, MinConnections: 1, DialTimeout: 5 * time.Second, StatementTimeout: 5 * time.Second, LockTimeout: time.Second, IdleTxTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := Install(ctx, pool, ns); err != nil {
		cancel()
		pool.Close()
		t.Fatal(err)
	}
	return MaintenanceRepository{Pool: pool, Namespace: ns}, ctx, func() { cancel(); pool.Close() }
}

func TestMaintenanceOutboxFencesReclaimedLeaseAndDeduplicatesCompletion(t *testing.T) {
	repository, ctx, cleanup := maintenanceIntegrationRepository(t)
	defer cleanup()
	now := time.Now().UTC().Truncate(time.Microsecond)
	event, err := maintenance.NewDeleteBlobEvent(uuid.New().String(), uuid.New().String(), now.Add(-time.Minute), now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishOutbox(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishOutbox(ctx, event); err != nil {
		t.Fatalf("duplicate enqueue was not idempotent: %v", err)
	}
	conflict := event
	conflict.ID = uuid.New().String()
	conflict.AggregateID = uuid.New().String()
	if err := repository.PublishOutbox(ctx, conflict); !errors.Is(err, ErrMaintenanceOutboxConflict) {
		t.Fatalf("conflicting dedupe enqueue was accepted: %v", err)
	}
	typeConflict := event
	typeConflict.ID = uuid.New().String()
	typeConflict.AggregateType = "provider_state"
	if err := repository.PublishOutbox(ctx, typeConflict); !errors.Is(err, ErrMaintenanceOutboxConflict) {
		t.Fatalf("aggregate-type dedupe conflict was accepted: %v", err)
	}
	first, err := repository.ClaimOutbox(ctx, maintenance.ClaimOptions{Now: now, Limit: 1, Lease: 10 * time.Minute})
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	outboxTable, err := repository.Namespace.Render("maintenance_outbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool.Exec(ctx, "UPDATE "+outboxTable+" SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE outbox_id = $1", uuid.MustParse(event.ID)); err != nil {
		t.Fatal(err)
	}
	second, err := repository.ClaimOutbox(ctx, maintenance.ClaimOptions{Now: now.Add(time.Second), Limit: 1, Lease: 10 * time.Minute})
	if err != nil || len(second) != 1 {
		t.Fatalf("reclaim=%+v err=%v", second, err)
	}
	if first[0].LeaseToken == second[0].LeaseToken {
		t.Fatal("reclaim reused the old lease fence")
	}
	if err := repository.CompleteOutbox(ctx, event.ID, first[0].LeaseToken, time.Now().UTC()); !errors.Is(err, maintenance.ErrOutboxNotClaimed) {
		t.Fatalf("stale claimant completed reclaimed row: %v", err)
	}
	if err := repository.CompleteOutbox(ctx, event.ID, second[0].LeaseToken, time.Now().UTC()); err != nil {
		t.Fatalf("current claimant completion failed: %v", err)
	}
	if err := repository.CompleteOutbox(ctx, event.ID, second[0].LeaseToken, time.Now().UTC()); err != nil {
		t.Fatalf("duplicate completion was not idempotent: %v", err)
	}
}

func TestMaintenanceRetentionPreservesCurrentProviderState(t *testing.T) {
	repository, ctx, cleanup := maintenanceIntegrationRepository(t)
	defer cleanup()
	now := time.Now().UTC().Truncate(time.Microsecond)
	configDigest := sha256.Sum256([]byte("maintenance-retention-" + now.Format(time.RFC3339Nano)))
	configs, err := repository.Namespace.Render("configuration_snapshots")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool.Exec(ctx, "INSERT INTO "+configs+" (config_digest, config_version, source_digest, sanitized_config) VALUES ($1,$2,$1,'{}'::jsonb)", configDigest[:], "maintenance-retention-"+now.Format("150405.000000")); err != nil {
		t.Fatal(err)
	}

	statusRepository := DefaultProviderStatusRepository(repository.Pool, repository.Namespace)
	statusBase := control.StatusObservation{
		ConfigDigest:        configDigest,
		RouteID:             "maintenance-retention-route",
		EndpointID:          "maintenance-retention-endpoint",
		EndpointAccountHMAC: sha256.Sum256([]byte("maintenance-retention-account")),
		Provider:            "maintenance-provider",
		EndpointFamily:      "chat",
		Source:              control.SourceInference,
		Availability:        control.AvailabilityAvailable,
		Credit:              control.CreditOK,
		Billing:             control.BillingOK,
		EvidenceDigest:      sha256.Sum256([]byte("maintenance-retention-evidence")),
		ConfigEpoch:         "maintenance-retention-epoch",
	}
	oldObservation := statusBase
	oldObservation.ObservedAt = now.Add(-3 * time.Hour)
	oldObservation.ExpiresAt = now.Add(-2 * time.Hour)
	oldEvent, err := control.NewStatusEvent(oldObservation)
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := statusRepository.PersistStatusEvent(ctx, oldEvent); err != nil || !applied {
		t.Fatalf("persist old status event: applied=%v err=%v", applied, err)
	}
	latestObservation := statusBase
	latestObservation.ObservedAt = now.Add(-90 * time.Minute)
	// The current projection may still reference an expired event. Retention
	// must preserve it until the projection advances, so the foreign key remains
	// valid even when the event is past its expiry cutoff.
	latestObservation.ExpiresAt = now.Add(-70 * time.Minute)
	latestObservation.EvidenceDigest = sha256.Sum256([]byte("maintenance-retention-latest"))
	latestEvent, err := control.NewStatusEvent(latestObservation)
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := statusRepository.PersistStatusEvent(ctx, latestEvent); err != nil || !applied {
		t.Fatalf("persist latest status event: applied=%v err=%v", applied, err)
	}

	inventoryRepository := DefaultInventoryRepository(repository.Pool, repository.Namespace, Keyring{Active: "maintenance-retention-v1", Keys: map[string][]byte{"maintenance-retention-v1": []byte("01234567890123456789012345678901")}})
	oldInventory := inventoryQuerySnapshot(configDigest, "maintenance-provider", "maintenance-retention-endpoint", now.Add(-3*time.Hour), "maintenance-old-model")
	oldInventory.ExpiresAt = now.Add(-2 * time.Hour)
	if _, err := inventoryRepository.PersistSnapshot(ctx, oldInventory); err != nil {
		t.Fatalf("persist old inventory snapshot: %v", err)
	}
	latestInventory := inventoryQuerySnapshot(configDigest, "maintenance-provider", "maintenance-retention-endpoint", now.Add(-30*time.Minute), "maintenance-latest-model")
	latestInventory.ExpiresAt = now.Add(time.Hour)
	if _, err := inventoryRepository.PersistSnapshot(ctx, latestInventory); err != nil {
		t.Fatalf("persist latest inventory snapshot: %v", err)
	}
	// The provider is part of the query's latest-snapshot identity. A newer
	// snapshot from another provider must not protect this provider's history.
	singletonInventory := inventoryQuerySnapshot(configDigest, "maintenance-other-provider", "maintenance-retention-endpoint", now.Add(-150*time.Minute), "maintenance-singleton-model")
	singletonInventory.ExpiresAt = now.Add(-2 * time.Hour)
	if _, err := inventoryRepository.PersistSnapshot(ctx, singletonInventory); err != nil {
		t.Fatalf("persist singleton inventory snapshot: %v", err)
	}

	cutoff := now.Add(-time.Hour)
	statusResult, err := repository.PruneExpiredProviderStatus(ctx, now, cutoff, 10)
	if err != nil {
		t.Fatalf("prune provider status: %v", err)
	}
	if statusResult.Examined != 1 || statusResult.Deleted != 1 {
		t.Fatalf("provider status retention result = %+v", statusResult)
	}
	inventoryResult, err := repository.PruneExpiredInventory(ctx, now, cutoff, 10)
	if err != nil {
		t.Fatalf("prune provider inventory: %v", err)
	}
	if inventoryResult.Examined != 1 || inventoryResult.Deleted != 1 {
		t.Fatalf("provider inventory retention result = %+v", inventoryResult)
	}

	statusEvents, err := repository.Namespace.Render("provider_status_events")
	if err != nil {
		t.Fatal(err)
	}
	var statusCount, currentStatusCount int
	if err := repository.Pool.QueryRow(ctx, "SELECT count(*) FROM "+statusEvents+" WHERE config_digest = $1 AND route_id = $2", configDigest[:], statusBase.RouteID).Scan(&statusCount); err != nil {
		t.Fatal(err)
	}
	routes, err := repository.Namespace.Render("provider_route_status")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Pool.QueryRow(ctx, "SELECT count(*) FROM "+statusEvents+" e JOIN "+routes+" r ON r.last_event_id = e.event_id WHERE r.config_digest = $1 AND r.route_id = $2", configDigest[:], statusBase.RouteID).Scan(&currentStatusCount); err != nil {
		t.Fatal(err)
	}
	if statusCount != 1 || currentStatusCount != 1 {
		t.Fatalf("status history/current projection counts = %d/%d, want 1/1", statusCount, currentStatusCount)
	}

	snapshots, err := repository.Namespace.Render("provider_inventory_snapshots")
	if err != nil {
		t.Fatal(err)
	}
	models, err := repository.Namespace.Render("provider_inventory_models")
	if err != nil {
		t.Fatal(err)
	}
	var snapshotCount, modelCount int
	if err := repository.Pool.QueryRow(ctx, "SELECT count(*) FROM "+snapshots+" WHERE config_digest = $1", configDigest[:]).Scan(&snapshotCount); err != nil {
		t.Fatal(err)
	}
	if err := repository.Pool.QueryRow(ctx, "SELECT count(*) FROM "+models+" m JOIN "+snapshots+" s ON s.inventory_snapshot_id = m.inventory_snapshot_id WHERE s.config_digest = $1", configDigest[:]).Scan(&modelCount); err != nil {
		t.Fatal(err)
	}
	if snapshotCount != 2 || modelCount != 2 {
		t.Fatalf("inventory snapshot/model counts = %d/%d, want 2/2", snapshotCount, modelCount)
	}
}
