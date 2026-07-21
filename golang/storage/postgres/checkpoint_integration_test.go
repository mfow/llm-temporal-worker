package postgres

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mfow/llm-temporal-worker/golang/admission"
	"github.com/mfow/llm-temporal-worker/golang/pricing"
	"github.com/mfow/llm-temporal-worker/golang/state"
)

func TestCheckpointRepositoryPublishReadAndRetry(t *testing.T) {
	operations, ctx, cleanup := operationIntegrationRepository(t)
	defer cleanup()
	scope, err := operations.Scopes.Ensure(ctx, "checkpoint-integration-tenant", "checkpoint-integration-project")
	if err != nil {
		t.Fatal(err)
	}
	operationID := "checkpoint-origin-" + uuid.NewString()
	origin, err := operations.Begin(ctx, admission.BeginRequest{
		ID: operationID, ScopeKey: "checkpoint-integration-tenant/checkpoint-integration-project",
		RequestDigest: admission.Digest([]byte(operationID)), ReservationUSD: pricing.MustUSD("0"),
		ExpiresAt: time.Now().UTC().Add(time.Hour), RequestManifest: []byte(`{"model":"fixture"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	blobs := BlobRepository{Pool: operations.Pool, Namespace: operations.Namespace, Keys: operations.Keys, NewID: UUIDv7}
	payload := []byte("checkpoint-fixture")
	digest := sha256.Sum256(payload)
	blob, err := blobs.PutLocator(ctx, scope.ID, "checkpoint-fixture", BlobMetadata{StoreID: "checkpoint-fixture-store", Digest: digest, ByteLength: int64(len(payload)), MediaType: "application/json"}, payload)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	checkpoint := state.DurableCheckpoint{
		ID: state.CheckpointID(uuid.NewString()), ScopeID: scope.ID.String(), PublicIDHMAC: sha256.Sum256([]byte("public")), HandleKeyID: operations.Keys.Active,
		Kind: state.CheckpointGeneration, Depth: 0, OriginOperationID: state.OperationID(operationUUID(origin.Operation.ID).String()),
		DeltaBlob:              state.CheckpointBlobReference{ID: state.BlobID(blob.BlobID.String()), Digest: digest, ByteLength: int64(len(payload)), MediaType: "application/json"},
		ResponseBlob:           state.CheckpointBlobReference{ID: state.BlobID(blob.BlobID.String()), Digest: digest, ByteLength: int64(len(payload)), MediaType: "application/json"},
		SettingsPatchBlob:      state.CheckpointBlobReference{ID: state.BlobID(blob.BlobID.String()), Digest: digest, ByteLength: int64(len(payload)), MediaType: "application/json"},
		CanonicalLineageDigest: sha256.Sum256([]byte("lineage")), MaterializedSettingsDigest: sha256.Sum256([]byte("settings")), ToolFrontierDigest: sha256.Sum256([]byte("frontier")),
		SchemaVersion: 1, CompilerEpoch: "checkpoint-test-v1", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	repository := DurableCheckpointRepository{Pool: operations.Pool, Namespace: operations.Namespace, Now: func() time.Time { return now }}
	if err := state.WithCheckpointUnitOfWork(ctx, repository, func(ctx context.Context, unit state.CheckpointUnitOfWork) error {
		return unit.PutCheckpoint(ctx, state.CheckpointWrite{Checkpoint: checkpoint})
	}); err != nil {
		t.Fatal(err)
	}
	read, err := repository.Get(ctx, checkpoint.ScopeID, checkpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.ID != checkpoint.ID || read.ScopeID != checkpoint.ScopeID || read.DeltaBlob != checkpoint.DeltaBlob || read.OriginOperationID != checkpoint.OriginOperationID {
		t.Fatalf("read checkpoint=%#v, want %#v", read, checkpoint)
	}
	if err := state.WithCheckpointUnitOfWork(ctx, repository, func(ctx context.Context, unit state.CheckpointUnitOfWork) error {
		return unit.PutCheckpoint(ctx, state.CheckpointWrite{Checkpoint: checkpoint})
	}); err != nil {
		t.Fatalf("same immutable retry failed: %v", err)
	}
	childConflict := checkpoint
	childConflict.ProviderState = []state.CheckpointProviderState{{
		Ordinal: 0, Provider: "fixture", EndpointID: "endpoint-fixture", EndpointAccountHMAC: sha256.Sum256([]byte("account")),
		Region: "test", EndpointFamily: "fixture", ModelLineage: "fixture-v1", StateKind: "opaque",
		StateBlob: checkpoint.DeltaBlob, StateDigest: digest, Required: true, ImmutableForkSafe: true, CreatedAt: now,
	}}
	err = state.WithCheckpointUnitOfWork(ctx, repository, func(ctx context.Context, unit state.CheckpointUnitOfWork) error {
		return unit.PutCheckpoint(ctx, state.CheckpointWrite{Checkpoint: childConflict})
	})
	if err != ErrCheckpointConflict {
		t.Fatalf("different child retry error=%v, want ErrCheckpointConflict", err)
	}
	conflict := checkpoint
	conflict.PublicIDHMAC = sha256.Sum256([]byte("different-public"))
	err = state.WithCheckpointUnitOfWork(ctx, repository, func(ctx context.Context, unit state.CheckpointUnitOfWork) error {
		return unit.PutCheckpoint(ctx, state.CheckpointWrite{Checkpoint: conflict})
	})
	if err != ErrCheckpointConflict {
		t.Fatalf("different immutable retry error=%v, want ErrCheckpointConflict", err)
	}
}

func TestCheckpointRepositoryRejectsCrossScopeCompactionReference(t *testing.T) {
	operations, ctx, cleanup := operationIntegrationRepository(t)
	defer cleanup()

	scope, err := operations.Scopes.Ensure(ctx, "checkpoint-scope-a", "checkpoint-project")
	if err != nil {
		t.Fatal(err)
	}
	otherScope, err := operations.Scopes.Ensure(ctx, "checkpoint-scope-b", "checkpoint-project")
	if err != nil {
		t.Fatal(err)
	}
	blobs := BlobRepository{Pool: operations.Pool, Namespace: operations.Namespace, Keys: operations.Keys, NewID: UUIDv7}
	makeOrigin := func(scopeKey string) (admission.BeginResult, error) {
		id := "checkpoint-cross-scope-" + uuid.NewString()
		return operations.Begin(ctx, admission.BeginRequest{
			ID: id, ScopeKey: scopeKey, RequestDigest: admission.Digest([]byte(id)), ReservationUSD: pricing.MustUSD("0"),
			ExpiresAt: time.Now().UTC().Add(time.Hour), RequestManifest: []byte(`{"model":"fixture"}`),
		})
	}
	originA, err := makeOrigin("checkpoint-scope-a/checkpoint-project")
	if err != nil {
		t.Fatal(err)
	}
	originB, err := makeOrigin("checkpoint-scope-b/checkpoint-project")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("checkpoint-cross-scope-fixture")
	digest := sha256.Sum256(payload)
	blobA, err := blobs.PutLocator(ctx, scope.ID, "checkpoint-cross-scope-a", BlobMetadata{StoreID: "checkpoint-cross-scope-store", Digest: digest, ByteLength: int64(len(payload)), MediaType: "application/json"}, payload)
	if err != nil {
		t.Fatal(err)
	}
	blobB, err := blobs.PutLocator(ctx, otherScope.ID, "checkpoint-cross-scope-b", BlobMetadata{StoreID: "checkpoint-cross-scope-store", Digest: digest, ByteLength: int64(len(payload)), MediaType: "application/json"}, payload)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	newCheckpoint := func(scopeID uuid.UUID, operationID string, blob BlobRecord, name string) state.DurableCheckpoint {
		return state.DurableCheckpoint{
			ID: state.CheckpointID(uuid.NewString()), ScopeID: scopeID.String(), PublicIDHMAC: sha256.Sum256([]byte(name)), HandleKeyID: operations.Keys.Active,
			Kind: state.CheckpointGeneration, Depth: 0, OriginOperationID: state.OperationID(operationUUID(operationID).String()),
			DeltaBlob:              state.CheckpointBlobReference{ID: state.BlobID(blob.BlobID.String()), Digest: digest, ByteLength: int64(len(payload)), MediaType: "application/json"},
			ResponseBlob:           state.CheckpointBlobReference{ID: state.BlobID(blob.BlobID.String()), Digest: digest, ByteLength: int64(len(payload)), MediaType: "application/json"},
			SettingsPatchBlob:      state.CheckpointBlobReference{ID: state.BlobID(blob.BlobID.String()), Digest: digest, ByteLength: int64(len(payload)), MediaType: "application/json"},
			CanonicalLineageDigest: sha256.Sum256([]byte(name + "-lineage")), MaterializedSettingsDigest: sha256.Sum256([]byte(name + "-settings")), ToolFrontierDigest: sha256.Sum256([]byte(name + "-frontier")),
			SchemaVersion: 1, CompilerEpoch: "checkpoint-cross-scope-v1", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		}
	}
	checkpointA := newCheckpoint(scope.ID, originA.Operation.ID, blobA, "checkpoint-a")
	checkpointB := newCheckpoint(otherScope.ID, originB.Operation.ID, blobB, "checkpoint-b")
	repository := DurableCheckpointRepository{Pool: operations.Pool, Namespace: operations.Namespace, Now: func() time.Time { return now }}
	for _, checkpoint := range []state.DurableCheckpoint{checkpointA, checkpointB} {
		if err := state.WithCheckpointUnitOfWork(ctx, repository, func(ctx context.Context, unit state.CheckpointUnitOfWork) error {
			return unit.PutCheckpoint(ctx, state.CheckpointWrite{Checkpoint: checkpoint})
		}); err != nil {
			t.Fatal(err)
		}
	}
	crossScope := checkpointA
	crossScope.ID = state.CheckpointID(uuid.NewString())
	crossScope.PublicIDHMAC = sha256.Sum256([]byte("cross-scope-compaction"))
	crossScope.CompactedThroughID = &checkpointB.ID
	err = state.WithCheckpointUnitOfWork(ctx, repository, func(ctx context.Context, unit state.CheckpointUnitOfWork) error {
		return unit.PutCheckpoint(ctx, state.CheckpointWrite{Checkpoint: crossScope})
	})
	if err == nil || !strings.Contains(err.Error(), "compacted-through checkpoint belongs to a different scope") {
		t.Fatalf("cross-scope compaction reference error=%v", err)
	}
}
