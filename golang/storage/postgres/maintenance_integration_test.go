package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
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
