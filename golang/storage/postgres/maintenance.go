package postgres

// PostgreSQL maintenance is intentionally kept out of runtime repositories.
// Every mutating method is bounded, uses the dedicated llmtw_maintenance role,
// and either locks candidates with SKIP LOCKED or updates one claimed outbox
// row. External object-store work is never performed while a SQL transaction
// is open.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mfow/llm-temporal-worker/golang/maintenance"
)

const maxMaintenanceBatch = 10_000

var (
	ErrMaintenanceOutboxNotClaimed = maintenance.ErrOutboxNotClaimed
	ErrMaintenanceInvalidPayload   = errors.New("maintenance outbox payload is invalid")
)

// MaintenanceRepository owns only maintenance-role operations. It is not
// used by the worker runtime, which must not receive DELETE privileges.
type MaintenanceRepository struct {
	Pool      *pgxpool.Pool
	Namespace Namespace
	NewID     func() (uuid.UUID, error)
}

func (repository MaintenanceRepository) validate() error {
	if repository.Pool == nil {
		return errors.New("maintenance PostgreSQL pool is nil")
	}
	return repository.Namespace.Validate()
}

func validateBatch(now time.Time, limit int) error {
	if now.IsZero() {
		return errors.New("maintenance time is required")
	}
	if limit <= 0 || limit > maxMaintenanceBatch {
		return fmt.Errorf("maintenance batch limit must be between 1 and %d", maxMaintenanceBatch)
	}
	return nil
}

// PruneExpiredCache tombstones an unused cache entry and publishes a durable
// blob-delete intent in the same transaction. Reference/fill checks are
// repeated in the locked candidate query; a concurrent reference therefore
// prevents tombstoning rather than racing a blob deletion.
func (repository MaintenanceRepository) PruneExpiredCache(ctx context.Context, now, unusedBefore time.Time, limit int) (maintenance.RetentionResult, error) {
	var result maintenance.RetentionResult
	if err := repository.validate(); err != nil {
		return result, err
	}
	if err := validateBatch(now, limit); err != nil {
		return result, err
	}
	if unusedBefore.IsZero() || unusedBefore.After(now) {
		return result, errors.New("cache unused cutoff must not be after maintenance time")
	}
	cacheTable, err := repository.Namespace.Render("response_cache_entries")
	if err != nil {
		return result, err
	}
	fillsTable, err := repository.Namespace.Render("response_cache_fills")
	if err != nil {
		return result, err
	}
	operationsTable, err := repository.Namespace.Render("operations")
	if err != nil {
		return result, err
	}
	checkpointsTable, err := repository.Namespace.Render("conversation_checkpoints")
	if err != nil {
		return result, err
	}
	outboxTable, err := repository.Namespace.Render("maintenance_outbox")
	if err != nil {
		return result, err
	}
	newID := repository.NewID
	if newID == nil {
		newID = uuid.NewRandom
	}
	err = WithTransaction(ctx, repository.Pool, func(ctx context.Context, tx pgx.Tx) error {
		query := "WITH candidates AS (" +
			" SELECT c.cache_entry_id, c.response_blob_id" +
			" FROM " + cacheTable + " c" +
			" WHERE c.state = 'ready' AND c.last_used_at < $1" +
			" AND NOT EXISTS (SELECT 1 FROM " + fillsTable + " f" +
			" WHERE f.cache_entry_id = c.cache_entry_id AND f.state = 'filling' AND f.lease_expires_at > $2)" +
			" AND NOT EXISTS (SELECT 1 FROM " + operationsTable + " o" +
			" WHERE o.cache_entry_id = c.cache_entry_id AND o.state NOT IN ('completed', 'definite_failed', 'canceled'))" +
			" AND NOT EXISTS (SELECT 1 FROM " + checkpointsTable + " cp" +
			" WHERE cp.origin_cache_entry_id = c.cache_entry_id AND cp.expires_at > $2)" +
			" ORDER BY c.last_used_at, c.cache_entry_id LIMIT $3 FOR UPDATE SKIP LOCKED" +
			"), tombstoned AS (" +
			" UPDATE " + cacheTable + " c SET state = 'tombstoned'" +
			" FROM candidates WHERE c.cache_entry_id = candidates.cache_entry_id" +
			" RETURNING c.cache_entry_id, c.response_blob_id" +
			") SELECT cache_entry_id, response_blob_id FROM tombstoned"
		rows, err := tx.Query(ctx, query, unusedBefore, now, limit)
		if err != nil {
			return redactPostgresError(fmt.Errorf("claim expired response cache entries: %w", err))
		}
		defer rows.Close()
		for rows.Next() {
			var cacheID uuid.UUID
			var blobID *uuid.UUID
			if err := rows.Scan(&cacheID, &blobID); err != nil {
				return fmt.Errorf("scan expired response cache entry: %w", err)
			}
			result.Examined++
			result.Tombstoned++
			if blobID == nil {
				continue
			}
			eventID, err := newID()
			if err != nil {
				return fmt.Errorf("generate cache deletion outbox ID: %w", err)
			}
			dedupe := sha256.Sum256([]byte("llm-temporal-worker/delete-blob/v1\x00" + blobID.String()))
			payload, err := json.Marshal(struct {
				BlobID string `json:"blob_id"`
			}{BlobID: blobID.String()})
			if err != nil {
				return fmt.Errorf("encode cache deletion outbox payload: %w", err)
			}
			if _, err := tx.Exec(ctx, "INSERT INTO "+outboxTable+
				" (outbox_id, event_kind, aggregate_type, aggregate_id, dedupe_key, safe_payload, state, attempt_count, available_at, created_at)"+
				" VALUES ($1, 'delete_blob', 'blob', $2, $3, $4, 'pending', 0, $5, $5)"+
				" ON CONFLICT (event_kind, dedupe_key) DO NOTHING", eventID, *blobID, dedupe[:], payload, now); err != nil {
				return redactPostgresError(fmt.Errorf("publish cache deletion outbox: %w", err))
			}
			_ = cacheID // returned for deterministic row accounting and future metrics.
		}
		if err := rows.Err(); err != nil {
			return redactPostgresError(fmt.Errorf("iterate expired response cache entries: %w", err))
		}
		return nil
	})
	if err != nil {
		return maintenance.RetentionResult{}, err
	}
	return result, nil
}

// PublishOutbox inserts idempotent safe intent. Duplicate event_kind/dedupe
// pairs are successful no-ops, so a retry cannot enqueue duplicate deletion.
func (repository MaintenanceRepository) PublishOutbox(ctx context.Context, event maintenance.Event) error {
	if err := repository.validate(); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return err
	}
	eventID, err := uuid.Parse(event.ID)
	if err != nil {
		return fmt.Errorf("maintenance outbox ID: %w", err)
	}
	aggregateID, err := uuid.Parse(event.AggregateID)
	if err != nil {
		return fmt.Errorf("maintenance outbox aggregate ID: %w", err)
	}
	outboxTable, err := repository.Namespace.Render("maintenance_outbox")
	if err != nil {
		return err
	}
	createdAt := event.CreatedAt
	if createdAt.IsZero() {
		createdAt = event.AvailableAt
	}
	_, err = repository.Pool.Exec(ctx, "INSERT INTO "+outboxTable+
		" (outbox_id, event_kind, aggregate_type, aggregate_id, dedupe_key, safe_payload, state, attempt_count, available_at, created_at)"+
		" VALUES ($1, $2, $3, $4, $5, $6, 'pending', 0, $7, $8)"+
		" ON CONFLICT (event_kind, dedupe_key) DO NOTHING", eventID, string(event.Kind), event.AggregateType, aggregateID, event.DedupeKey[:], event.SafePayload, event.AvailableAt, createdAt)
	if err != nil {
		return redactPostgresError(fmt.Errorf("publish maintenance outbox: %w", err))
	}
	return nil
}

// ClaimOutbox uses a short transaction and FOR UPDATE SKIP LOCKED. Expired
// processing leases are eligible for recovery; live leases are untouched.
func (repository MaintenanceRepository) ClaimOutbox(ctx context.Context, options maintenance.ClaimOptions) ([]maintenance.Event, error) {
	if err := repository.validate(); err != nil {
		return nil, err
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	outboxTable, err := repository.Namespace.Render("maintenance_outbox")
	if err != nil {
		return nil, err
	}
	var events []maintenance.Event
	err = WithTransaction(ctx, repository.Pool, func(ctx context.Context, tx pgx.Tx) error {
		query := "WITH candidates AS (" +
			" SELECT outbox_id FROM " + outboxTable +
			" WHERE ((state IN ('pending', 'failed') AND available_at <= $1)" +
			" OR (state = 'processing' AND lease_expires_at <= $1))" +
			" ORDER BY available_at, outbox_id LIMIT $2 FOR UPDATE SKIP LOCKED" +
			"), claimed AS (" +
			" UPDATE " + outboxTable + " o SET state = 'processing', attempt_count = o.attempt_count + 1, lease_expires_at = $3" +
			" FROM candidates WHERE o.outbox_id = candidates.outbox_id" +
			" RETURNING o.outbox_id, o.event_kind, o.aggregate_type, o.aggregate_id, o.dedupe_key," +
			" o.safe_payload, o.state, o.attempt_count, o.available_at, o.lease_expires_at, o.created_at, o.completed_at" +
			") SELECT outbox_id, event_kind, aggregate_type, aggregate_id, dedupe_key, safe_payload, state," +
			" attempt_count, available_at, lease_expires_at, created_at, completed_at FROM claimed"
		rows, err := tx.Query(ctx, query, options.Now, options.Limit, options.Now.Add(options.Lease))
		if err != nil {
			return redactPostgresError(fmt.Errorf("claim maintenance outbox: %w", err))
		}
		defer rows.Close()
		for rows.Next() {
			var id, aggregateID uuid.UUID
			var kind, aggregateType, state string
			var dedupe, payload []byte
			var attempt int
			var available, created time.Time
			var lease, completed *time.Time
			if err := rows.Scan(&id, &kind, &aggregateType, &aggregateID, &dedupe, &payload, &state, &attempt, &available, &lease, &created, &completed); err != nil {
				return fmt.Errorf("scan claimed maintenance outbox: %w", err)
			}
			if len(dedupe) != 32 {
				return ErrMaintenanceInvalidPayload
			}
			var key [32]byte
			copy(key[:], dedupe)
			event := maintenance.Event{ID: id.String(), Kind: maintenance.EventKind(kind), AggregateType: aggregateType, AggregateID: aggregateID.String(), DedupeKey: key, SafePayload: append([]byte(nil), payload...), State: maintenance.EventState(state), AttemptCount: attempt, AvailableAt: available, CreatedAt: created}
			if lease != nil {
				event.LeaseExpiresAt = *lease
			}
			if completed != nil {
				event.CompletedAt = *completed
			}
			if err := event.Validate(); err != nil {
				return fmt.Errorf("validate claimed maintenance outbox: %w", err)
			}
			events = append(events, event)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return events, nil
}

func (repository MaintenanceRepository) CompleteOutbox(ctx context.Context, id string, completedAt time.Time) error {
	return repository.updateOutboxState(ctx, id, completedAt, "completed", true)
}

func (repository MaintenanceRepository) RetryOutbox(ctx context.Context, id string, availableAt time.Time) error {
	return repository.updateOutboxState(ctx, id, availableAt, "failed", false)
}

func (repository MaintenanceRepository) updateOutboxState(ctx context.Context, id string, at time.Time, state string, completed bool) error {
	if err := repository.validate(); err != nil {
		return err
	}
	if id == "" || at.IsZero() {
		return errors.New("maintenance outbox state identity and time are required")
	}
	outboxID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("maintenance outbox ID: %w", err)
	}
	outboxTable, err := repository.Namespace.Render("maintenance_outbox")
	if err != nil {
		return err
	}
	query := "UPDATE " + outboxTable + " SET state = $2, lease_expires_at = NULL, available_at = CASE WHEN $3 THEN available_at ELSE $4 END, completed_at = CASE WHEN $3 THEN $4 ELSE NULL END WHERE outbox_id = $1 AND state = 'processing'"
	tag, err := repository.Pool.Exec(ctx, query, outboxID, state, completed, at)
	if err != nil {
		return redactPostgresError(fmt.Errorf("update maintenance outbox: %w", err))
	}
	if tag.RowsAffected() != 1 {
		return ErrMaintenanceOutboxNotClaimed
	}
	return nil
}
