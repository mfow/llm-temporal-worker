package postgres

// Inventory persistence stores one immutable, validated provider listing and
// its normalized model rows. Continuation cursors are envelope-encrypted and
// authenticated to the configuration, endpoint account, snapshot identity,
// and inventory digest; plaintext cursors never enter PostgreSQL.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mfow/llm-temporal-worker/golang/control"
)

var ErrInventorySnapshotNotFound = errors.New("provider inventory snapshot not found")

type InventoryRecord struct {
	ID       uuid.UUID
	Snapshot control.InventorySnapshot
	Cursor   SealedValue
}

type InventoryRepository struct {
	Pool      *pgxpool.Pool
	Namespace Namespace
	Keys      Keyring
	NewID     func() (uuid.UUID, error)
}

func DefaultInventoryRepository(pool *pgxpool.Pool, namespace Namespace, keys Keyring) InventoryRepository {
	return InventoryRepository{Pool: pool, Namespace: namespace, Keys: keys, NewID: UUIDv7}
}

func (repository InventoryRepository) validate() error {
	if repository.Pool == nil {
		return errors.New("inventory repository pool is nil")
	}
	if err := repository.Namespace.Validate(); err != nil {
		return err
	}
	if _, _, err := repository.Keys.activeKey(); err != nil {
		return err
	}
	if repository.NewID == nil {
		return errors.New("inventory repository UUID generator is nil")
	}
	return nil
}

func validateInventoryModels(snapshot control.InventorySnapshot) error {
	if snapshot.InventoryDigest != control.InventoryDigest(snapshot.Models) {
		return errors.New("inventory snapshot digest does not match its models")
	}
	for _, model := range snapshot.Models {
		if strings.TrimSpace(model.ProviderModelID) != model.ProviderModelID || strings.ContainsAny(model.ProviderModelID, "\x00\r\n") {
			return fmt.Errorf("inventory model %q contains unsafe identifier characters", model.ProviderModelID)
		}
		for name, value := range map[string]string{"display_name": model.DisplayName, "owned_by": model.OwnedBy} {
			if len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
				return fmt.Errorf("inventory model %q %s is too long or unsafe", model.ProviderModelID, name)
			}
		}
	}
	return nil
}

// inventoryCursorContext derives a non-secret scope identity from the
// configuration and endpoint identity. The account HMAC, endpoint, snapshot
// UUID, and inventory digest are all authenticated by the AEAD context.
func inventoryCursorContext(snapshot control.InventorySnapshot, snapshotID uuid.UUID) EnvelopeContext {
	seed := make([]byte, 0, 96+len(snapshot.EndpointID))
	seed = append(seed, []byte("llmtw/inventory-scope/v1\x00")...)
	seed = append(seed, snapshot.ConfigDigest[:]...)
	seed = append(seed, snapshot.EndpointAccountHMAC[:]...)
	seed = append(seed, []byte(snapshot.EndpointID)...)
	scopeID := uuid.NewSHA1(uuid.NameSpaceOID, seed)
	return EnvelopeContext{ScopeID: scopeID, OperationID: snapshotID, PayloadKind: "inventory-cursor", Digest: snapshot.InventoryDigest}
}

// PersistSnapshot stores a validated listing and all model rows atomically.
// Repeating the same config/endpoint/observation with the same digest is an
// idempotent replay; a different digest for that identity is rejected.
func (repository InventoryRepository) PersistSnapshot(ctx context.Context, snapshot control.InventorySnapshot) (InventoryRecord, error) {
	var record InventoryRecord
	if err := repository.validate(); err != nil {
		return record, err
	}
	if err := snapshot.Validate(); err != nil {
		return record, err
	}
	if err := validateInventoryModels(snapshot); err != nil {
		return record, err
	}
	var err error
	record.ID, err = repository.NewID()
	if err != nil {
		return InventoryRecord{}, fmt.Errorf("generate inventory snapshot id: %w", err)
	}
	if record.ID == uuid.Nil {
		return InventoryRecord{}, errors.New("inventory repository UUID generator returned nil")
	}
	if snapshot.NextCursor != "" {
		if strings.ContainsAny(snapshot.NextCursor, "\x00\r\n") {
			return InventoryRecord{}, errors.New("inventory cursor contains unsafe characters")
		}
		sealed, err := repository.Keys.Seal(inventoryCursorContext(snapshot, record.ID), []byte(snapshot.NextCursor))
		if err != nil {
			return InventoryRecord{}, err
		}
		record.Cursor = sealed
	}
	snapshots, err := repository.Namespace.Render("provider_inventory_snapshots")
	if err != nil {
		return InventoryRecord{}, err
	}
	models, err := repository.Namespace.Render("provider_inventory_models")
	if err != nil {
		return InventoryRecord{}, err
	}
	tx, err := repository.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return InventoryRecord{}, redactPostgresError(fmt.Errorf("begin inventory transaction: %w", err))
	}
	defer tx.Rollback(ctx)
	insert := "INSERT INTO " + snapshots + " (inventory_snapshot_id, config_digest, provider, endpoint_id, endpoint_account_hmac, endpoint_family, region, source, observed_at, complete, next_cursor_ciphertext, next_cursor_key_id, inventory_digest, expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT (config_digest, endpoint_id, endpoint_account_hmac, observed_at) DO NOTHING RETURNING inventory_snapshot_id"
	var insertedID uuid.UUID
	err = tx.QueryRow(ctx, insert, record.ID, snapshot.ConfigDigest[:], snapshot.Provider, snapshot.EndpointID, snapshot.EndpointAccountHMAC[:], snapshot.EndpointFamily, snapshot.Region, snapshot.Source, snapshot.ObservedAt, snapshot.Complete, nullableBytes(record.Cursor.Ciphertext), nullableString(record.Cursor.KeyID), snapshot.InventoryDigest[:], snapshot.ExpiresAt).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		var existingDigest, existingCiphertext []byte
		var existingKeyID *string
		if err := tx.QueryRow(ctx, "SELECT inventory_snapshot_id, inventory_digest, next_cursor_ciphertext, next_cursor_key_id FROM "+snapshots+" WHERE config_digest=$1 AND endpoint_id=$2 AND endpoint_account_hmac=$3 AND observed_at=$4", snapshot.ConfigDigest[:], snapshot.EndpointID, snapshot.EndpointAccountHMAC[:], snapshot.ObservedAt).Scan(&record.ID, &existingDigest, &existingCiphertext, &existingKeyID); err != nil {
			return InventoryRecord{}, redactPostgresError(fmt.Errorf("read existing inventory snapshot: %w", err))
		}
		if len(existingCiphertext) > 0 {
			if existingKeyID == nil || *existingKeyID == "" {
				return InventoryRecord{}, errors.New("existing inventory cursor key id is missing")
			}
			record.Cursor = SealedValue{KeyID: *existingKeyID, Ciphertext: append([]byte(nil), existingCiphertext...)}
			record.Cursor.ContextHash, _ = contextDigest(inventoryCursorContext(snapshot, record.ID))
		}
		if len(existingDigest) != len(snapshot.InventoryDigest) || string(existingDigest) != string(snapshot.InventoryDigest[:]) {
			return InventoryRecord{}, errors.New("inventory observation already exists with a different digest")
		}
		record.Snapshot = snapshot
		if err := tx.Commit(ctx); err != nil {
			return InventoryRecord{}, redactPostgresError(fmt.Errorf("commit inventory replay: %w", err))
		}
		return record, nil
	}
	if err != nil {
		return InventoryRecord{}, redactPostgresError(fmt.Errorf("insert inventory snapshot: %w", err))
	}
	if insertedID != record.ID {
		return InventoryRecord{}, errors.New("PostgreSQL inventory snapshot identity changed")
	}
	for _, model := range snapshot.Models {
		metadata := []byte(`{}`)
		if model.SafeMetadata != nil {
			var err error
			metadata, err = json.Marshal(model.SafeMetadata)
			if err != nil {
				return InventoryRecord{}, fmt.Errorf("encode inventory model metadata: %w", err)
			}
		}
		var createdAt *time.Time
		if !model.CreatedAt.IsZero() {
			value := model.CreatedAt.UTC()
			createdAt = &value
		}
		var displayName, ownedBy *string
		if model.DisplayName != "" {
			value := model.DisplayName
			displayName = &value
		}
		if model.OwnedBy != "" {
			value := model.OwnedBy
			ownedBy = &value
		}
		if _, err := tx.Exec(ctx, "INSERT INTO "+models+" (inventory_snapshot_id, provider_model_id, display_name, owned_by, created_at_provider, lifecycle_state, capability_digest, safe_metadata) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb)", record.ID, model.ProviderModelID, displayName, ownedBy, createdAt, model.Lifecycle, model.CapabilityDigest[:], metadata); err != nil {
			return InventoryRecord{}, redactPostgresError(fmt.Errorf("insert inventory model: %w", err))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return InventoryRecord{}, redactPostgresError(fmt.Errorf("commit inventory snapshot: %w", err))
	}
	record.Snapshot = snapshot
	return record, nil
}

func nullableBytes(value []byte) []byte {
	if len(value) == 0 {
		return nil
	}
	return value
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// OpenCursor authenticates and decrypts the continuation cursor for a record.
func (repository InventoryRepository) OpenCursor(record InventoryRecord) (string, error) {
	if err := repository.validate(); err != nil {
		return "", err
	}
	if record.ID == uuid.Nil {
		return "", errors.New("inventory snapshot id is required")
	}
	if len(record.Cursor.Ciphertext) == 0 {
		return "", nil
	}
	plaintext, err := repository.Keys.Open(inventoryCursorContext(record.Snapshot, record.ID), record.Cursor)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(string(plaintext), "\x00\r\n") {
		return "", errors.New("inventory cursor contains unsafe characters")
	}
	return string(plaintext), nil
}

// GetSnapshot loads one immutable snapshot and its model rows.
func (repository InventoryRepository) GetSnapshot(ctx context.Context, snapshotID uuid.UUID) (InventoryRecord, error) {
	var record InventoryRecord
	if err := repository.validate(); err != nil {
		return record, err
	}
	if snapshotID == uuid.Nil {
		return record, errors.New("inventory snapshot id is required")
	}
	snapshots, err := repository.Namespace.Render("provider_inventory_snapshots")
	if err != nil {
		return record, err
	}
	models, err := repository.Namespace.Render("provider_inventory_models")
	if err != nil {
		return record, err
	}
	var configDigest, account, inventoryDigest, ciphertext []byte
	var cursorKeyID *string
	var source string
	if err := repository.Pool.QueryRow(ctx, "SELECT inventory_snapshot_id, config_digest, provider, endpoint_id, endpoint_account_hmac, endpoint_family, region, source, observed_at, complete, next_cursor_ciphertext, next_cursor_key_id, inventory_digest, expires_at FROM "+snapshots+" WHERE inventory_snapshot_id=$1", snapshotID).Scan(&record.ID, &configDigest, &record.Snapshot.Provider, &record.Snapshot.EndpointID, &account, &record.Snapshot.EndpointFamily, &record.Snapshot.Region, &source, &record.Snapshot.ObservedAt, &record.Snapshot.Complete, &ciphertext, &cursorKeyID, &inventoryDigest, &record.Snapshot.ExpiresAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InventoryRecord{}, ErrInventorySnapshotNotFound
		}
		return InventoryRecord{}, redactPostgresError(fmt.Errorf("get inventory snapshot: %w", err))
	}
	if len(configDigest) != 32 || len(account) != 32 || len(inventoryDigest) != 32 {
		return InventoryRecord{}, errors.New("PostgreSQL inventory snapshot has invalid digest length")
	}
	copy(record.Snapshot.ConfigDigest[:], configDigest)
	copy(record.Snapshot.EndpointAccountHMAC[:], account)
	copy(record.Snapshot.InventoryDigest[:], inventoryDigest)
	record.Snapshot.Source = control.InventorySource(source)
	if len(ciphertext) > 0 {
		if cursorKeyID == nil || *cursorKeyID == "" {
			return InventoryRecord{}, errors.New("PostgreSQL inventory cursor key id is missing")
		}
		record.Cursor = SealedValue{KeyID: *cursorKeyID, Ciphertext: append([]byte(nil), ciphertext...)}
		record.Cursor.ContextHash, _ = contextDigest(inventoryCursorContext(record.Snapshot, record.ID))
		cursor, err := repository.Keys.Open(inventoryCursorContext(record.Snapshot, record.ID), record.Cursor)
		if err != nil {
			return InventoryRecord{}, fmt.Errorf("open persisted inventory cursor: %w", err)
		}
		if len(cursor) > 2048 || strings.ContainsAny(string(cursor), "\x00\r\n") {
			return InventoryRecord{}, errors.New("persisted inventory cursor is unsafe")
		}
		record.Snapshot.NextCursor = string(cursor)
	}
	rows, err := repository.Pool.Query(ctx, "SELECT provider_model_id, COALESCE(display_name,''), COALESCE(owned_by,''), created_at_provider, lifecycle_state, capability_digest, safe_metadata FROM "+models+" WHERE inventory_snapshot_id=$1 ORDER BY provider_model_id", snapshotID)
	if err != nil {
		return InventoryRecord{}, redactPostgresError(fmt.Errorf("list inventory models: %w", err))
	}
	defer rows.Close()
	for rows.Next() {
		var model control.Model
		var capability []byte
		var metadata []byte
		var createdAt *time.Time
		if err := rows.Scan(&model.ProviderModelID, &model.DisplayName, &model.OwnedBy, &createdAt, &model.Lifecycle, &capability, &metadata); err != nil {
			return InventoryRecord{}, redactPostgresError(fmt.Errorf("scan inventory model: %w", err))
		}
		if createdAt != nil {
			model.CreatedAt = createdAt.UTC()
		}
		if len(capability) != 32 {
			return InventoryRecord{}, errors.New("PostgreSQL inventory model has invalid capability digest length")
		}
		copy(model.CapabilityDigest[:], capability)
		if string(metadata) == "null" {
			return InventoryRecord{}, errors.New("PostgreSQL inventory model metadata is null")
		}
		if err := json.Unmarshal(metadata, &model.SafeMetadata); err != nil {
			return InventoryRecord{}, fmt.Errorf("decode inventory model metadata: %w", err)
		}
		record.Snapshot.Models = append(record.Snapshot.Models, model)
	}
	if err := rows.Err(); err != nil {
		return InventoryRecord{}, redactPostgresError(fmt.Errorf("read inventory models: %w", err))
	}
	if err := record.Snapshot.Validate(); err != nil {
		return InventoryRecord{}, fmt.Errorf("validate persisted inventory snapshot: %w", err)
	}
	if err := validateInventoryModels(record.Snapshot); err != nil {
		return InventoryRecord{}, err
	}
	return record, nil
}
