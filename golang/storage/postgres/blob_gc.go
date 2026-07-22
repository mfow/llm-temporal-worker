package postgres

// Blob garbage collection is deliberately separate from the cache and
// outbox repository.  It provides the small SQL state machine an external
// object-store worker needs: mark expired, unreferenced blobs eligible;
// claim a bounded set with a fence; and finalize a successful delete.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrBlobDeletionNotClaimed = errors.New("blob deletion was not claimed")

// BlobDeletionClaim is the metadata needed by an object-store deleter.  The
// locator remains sealed; opening it is an explicit operation outside the SQL
// transaction used to claim the row.
type BlobDeletionClaim struct {
	BlobRecord
}

// BlobGCResult reports one bounded eligibility pass.  Examined and Eligible
// are counts for this invocation, not estimates of the complete table.
type BlobGCResult struct {
	Examined int
	Eligible int
	Skipped  int
}

// blobReferenceFreeSQL returns the conservative retained-reference checks
// shared by eligibility and claim queries.  Tombstoned cache rows are
// logically dead; all active/retained operation, checkpoint, provider-state,
// and cache paths continue to fence physical deletion.
func blobReferenceFreeSQL(blobs, operations, checkpoints, providerState, cacheEntries, cacheUses, cacheFills string) string {
	return "NOT EXISTS (SELECT 1 FROM " + operations + " o" +
		" WHERE (o.request_blob_id = b.blob_id OR o.result_blob_id = b.blob_id)" +
		" AND (o.state NOT IN ('completed', 'definite_failed', 'canceled')" +
		" OR o.retention_expires_at IS NULL OR o.retention_expires_at > $1))" +
		" AND NOT EXISTS (SELECT 1 FROM " + checkpoints + " cp" +
		" WHERE (cp.delta_blob_id = b.blob_id OR cp.response_blob_id = b.blob_id" +
		" OR cp.settings_patch_blob_id = b.blob_id OR cp.materialized_snapshot_blob_id = b.blob_id)" +
		" AND cp.expires_at > $1)" +
		" AND NOT EXISTS (SELECT 1 FROM " + providerState + " ps" +
		" WHERE ps.state_blob_id = b.blob_id" +
		" AND (ps.expires_at IS NULL OR ps.expires_at > $1))" +
		" AND NOT EXISTS (SELECT 1 FROM " + cacheEntries + " c" +
		" WHERE c.response_blob_id = b.blob_id AND c.state = 'ready')" +
		" AND NOT EXISTS (SELECT 1 FROM " + cacheEntries + " c" +
		" JOIN " + operations + " o ON o.cache_entry_id = c.cache_entry_id" +
		" WHERE c.response_blob_id = b.blob_id" +
		" AND (o.state NOT IN ('completed', 'definite_failed', 'canceled')" +
		" OR o.retention_expires_at IS NULL OR o.retention_expires_at > $1))" +
		" AND NOT EXISTS (SELECT 1 FROM " + cacheEntries + " c" +
		" JOIN " + checkpoints + " cp ON cp.origin_cache_entry_id = c.cache_entry_id" +
		" WHERE c.response_blob_id = b.blob_id AND cp.expires_at > $1)" +
		" AND NOT EXISTS (SELECT 1 FROM " + cacheUses + " u" +
		" JOIN " + operations + " o ON o.operation_id = u.operation_id" +
		" JOIN " + cacheEntries + " c ON c.cache_entry_id = u.cache_entry_id" +
		" WHERE c.response_blob_id = b.blob_id" +
		" AND (o.state NOT IN ('completed', 'definite_failed', 'canceled')" +
		" OR o.retention_expires_at IS NULL OR o.retention_expires_at > $1))" +
		" AND NOT EXISTS (SELECT 1 FROM " + cacheFills + " f" +
		" JOIN " + cacheEntries + " c ON c.cache_entry_id = f.cache_entry_id" +
		" WHERE c.response_blob_id = b.blob_id AND f.state = 'filling' AND f.lease_expires_at > $1)"
}

func (repository MaintenanceRepository) blobGCTables() (map[string]string, error) {
	if err := repository.validate(); err != nil {
		return nil, err
	}
	logical := []string{"blobs", "operations", "conversation_checkpoints", "checkpoint_provider_state", "response_cache_entries", "response_cache_uses", "response_cache_fills"}
	tables := make(map[string]string, len(logical))
	for _, name := range logical {
		relation, err := repository.Namespace.Render(name)
		if err != nil {
			return nil, err
		}
		tables[name] = relation
	}
	return tables, nil
}

// MarkExpiredBlobsEligible performs one bounded, locked pass.  Expiration is
// only a candidate signal: every retained reference is rechecked while the
// row is locked before the state changes.
func (repository MaintenanceRepository) MarkExpiredBlobsEligible(ctx context.Context, now time.Time, limit int) (BlobGCResult, error) {
	var result BlobGCResult
	if err := validateBatch(now, limit); err != nil {
		return result, err
	}
	tables, err := repository.blobGCTables()
	if err != nil {
		return result, err
	}
	refs := blobReferenceFreeSQL(tables["blobs"], tables["operations"], tables["conversation_checkpoints"], tables["checkpoint_provider_state"], tables["response_cache_entries"], tables["response_cache_uses"], tables["response_cache_fills"])
	query := "WITH candidates AS (SELECT b.blob_id FROM " + tables["blobs"] + " b" +
		" WHERE b.deletion_state = 'retained' AND b.expires_at IS NOT NULL AND b.expires_at <= $1 AND " + refs +
		" ORDER BY b.expires_at, b.blob_id LIMIT $2 FOR UPDATE OF b SKIP LOCKED), marked AS (" +
		" UPDATE " + tables["blobs"] + " b SET deletion_state = 'eligible' FROM candidates c" +
		" WHERE b.blob_id = c.blob_id RETURNING b.blob_id) SELECT blob_id FROM marked"
	err = WithTransaction(ctx, repository.Pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, now, limit)
		if err != nil {
			return redactPostgresError(fmt.Errorf("mark expired blobs eligible: %w", err))
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan eligible blob: %w", err)
			}
			result.Examined++
			result.Eligible++
		}
		return rows.Err()
	})
	if err != nil {
		return BlobGCResult{}, err
	}
	return result, nil
}

// ClaimBlobDeletion fences a bounded set of blobs as deleting.  Passing IDs
// claims explicit outbox targets (including non-expiring cache blobs); an
// empty ID list claims previously marked eligible rows.  The reference checks
// are repeated under FOR UPDATE SKIP LOCKED, so stale outbox work is harmless.
func (repository MaintenanceRepository) ClaimBlobDeletion(ctx context.Context, now time.Time, ids []uuid.UUID, limit int) ([]BlobDeletionClaim, error) {
	if err := validateBatch(now, limit); err != nil {
		return nil, err
	}
	tables, err := repository.blobGCTables()
	if err != nil {
		return nil, err
	}
	refs := blobReferenceFreeSQL(tables["blobs"], tables["operations"], tables["conversation_checkpoints"], tables["checkpoint_provider_state"], tables["response_cache_entries"], tables["response_cache_uses"], tables["response_cache_fills"])
	statePredicate := "b.deletion_state = 'eligible'"
	var args []any
	args = append(args, now)
	if len(ids) > 0 {
		statePredicate = "b.blob_id = ANY($2::uuid[]) AND b.deletion_state IN ('retained', 'eligible')"
		args = append(args, ids)
	}
	args = append(args, limit)
	limitArg := "$2"
	if len(ids) > 0 {
		limitArg = "$3"
	}
	query := "WITH candidates AS (SELECT b.blob_id FROM " + tables["blobs"] + " b WHERE " + statePredicate + " AND " + refs +
		" ORDER BY b.expires_at NULLS LAST, b.blob_id LIMIT " + limitArg + " FOR UPDATE OF b SKIP LOCKED), claimed AS (" +
		" UPDATE " + tables["blobs"] + " b SET deletion_state = 'deleting' FROM candidates c" +
		" WHERE b.blob_id = c.blob_id RETURNING b.blob_id, b.scope_id, b.store_id, b.sha256, b.byte_length, b.media_type, b.expires_at, b.created_at, b.deletion_state, b.locator_key_id, b.locator_ciphertext, b.encryption_context_digest)" +
		" SELECT blob_id, scope_id, store_id, sha256, byte_length, media_type, expires_at, created_at, deletion_state, locator_key_id, locator_ciphertext, encryption_context_digest FROM claimed"
	var claims []BlobDeletionClaim
	err = WithTransaction(ctx, repository.Pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return redactPostgresError(fmt.Errorf("claim blob deletion: %w", err))
		}
		defer rows.Close()
		for rows.Next() {
			claim, err := scanBlobDeletionClaim(rows)
			if err != nil {
				return err
			}
			claims = append(claims, claim)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return claims, nil
}

type blobClaimRow interface {
	Scan(...any) error
}

func scanBlobDeletionClaim(row blobClaimRow) (BlobDeletionClaim, error) {
	var claim BlobDeletionClaim
	var digest, contextHash []byte
	if err := row.Scan(&claim.BlobID, &claim.ScopeID, &claim.StoreID, &digest, &claim.ByteLength, &claim.MediaType, &claim.ExpiresAt, &claim.CreatedAt, &claim.DeletionState, &claim.LocatorKeyID, &claim.Locator.Ciphertext, &contextHash); err != nil {
		return claim, fmt.Errorf("scan blob deletion claim: %w", err)
	}
	if len(digest) != keyDigestBytes || len(contextHash) != keyDigestBytes {
		return claim, errors.New("blob deletion claim has invalid digest length")
	}
	copy(claim.Digest[:], digest)
	copy(claim.Locator.ContextHash[:], contextHash)
	claim.Locator.KeyID = claim.LocatorKeyID
	claim.Locator.Ciphertext = append([]byte(nil), claim.Locator.Ciphertext...)
	return claim, nil
}

// FinalizeBlobDeletion records successful physical deletion.  A missing
// metadata row (or an already-finalized row) is an idempotent success, which
// also makes an object-store not-found result safe to acknowledge.
func (repository MaintenanceRepository) FinalizeBlobDeletion(ctx context.Context, blobID uuid.UUID) error {
	if err := repository.validate(); err != nil {
		return err
	}
	if blobID == uuid.Nil {
		return errors.New("blob id is required")
	}
	blobs, err := repository.Namespace.Render("blobs")
	if err != nil {
		return err
	}
	return WithTransaction(ctx, repository.Pool, func(ctx context.Context, tx pgx.Tx) error {
		var state string
		err := tx.QueryRow(ctx, "SELECT deletion_state FROM "+blobs+" WHERE blob_id = $1 FOR UPDATE", blobID).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) || state == "deleted" {
			return nil
		}
		if err != nil {
			return redactPostgresError(fmt.Errorf("inspect blob deletion state: %w", err))
		}
		if state != "deleting" {
			return fmt.Errorf("%w: state is %q", ErrBlobDeletionNotClaimed, state)
		}
		if _, err := tx.Exec(ctx, "UPDATE "+blobs+" SET deletion_state = 'deleted' WHERE blob_id = $1 AND deletion_state = 'deleting'", blobID); err != nil {
			return redactPostgresError(fmt.Errorf("finalize blob deletion: %w", err))
		}
		return nil
	})
}
