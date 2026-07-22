package postgres

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mfow/llm-temporal-worker/golang/admission"
	"github.com/mfow/llm-temporal-worker/golang/pricing"
)

func TestBlobGCRechecksRetainedReferencesAndFinalizesIdempotently(t *testing.T) {
	fixture := newResponseCacheFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	maintenance := MaintenanceRepository{Pool: fixture.operations.Pool, Namespace: fixture.operations.Namespace}
	blobs := BlobRepository{Pool: fixture.operations.Pool, Namespace: fixture.operations.Namespace, Keys: fixture.operations.Keys, NewID: UUIDv7}
	put := func(name string) BlobRecord {
		payload := []byte("blob-gc-" + name + "-" + uuid.NewString())
		digest := sha256.Sum256(payload)
		expires := now.Add(-time.Minute)
		record, err := blobs.PutLocator(fixture.ctx, fixture.scope.ID, "blob-gc-test", BlobMetadata{StoreID: "blob-gc-" + name + "-" + uuid.NewString(), Digest: digest, ByteLength: int64(len(payload)), MediaType: "application/octet-stream", ExpiresAt: &expires}, payload)
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	free := put("free")
	operationBlob := put("operation")
	checkpointBlob := put("checkpoint")
	providerBlob := put("provider")
	cacheBlob := put("cache")

	// An active operation request keeps its blob retained.
	operation, err := fixture.operations.Begin(fixture.ctx, admission.BeginRequest{
		ID: "blob-gc-operation-" + uuid.NewString(), ScopeKey: "cache-integration-tenant/cache-integration-project",
		RequestDigest: admission.Digest([]byte("blob-gc-operation")), ReservationUSD: pricing.MustUSD("0"),
		ExpiresAt: now.Add(time.Hour), RequestManifest: []byte(`{"model":"fixture"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	operations, err := fixture.operations.Namespace.Render("operations")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.operations.Pool.Exec(fixture.ctx, "UPDATE "+operations+" SET request_inline_ciphertext=NULL, request_key_id=NULL, request_blob_id=$1 WHERE operation_id=$2", operationBlob.BlobID, operationUUID(operation.Operation.ID)); err != nil {
		t.Fatal(err)
	}

	// The fixture checkpoint keeps a blob through its future expiry. Replacing
	// the metadata expiry with a past value proves reference checks win over the
	// blob's own expiry signal.
	checkpoints, err := fixture.operations.Namespace.Render("conversation_checkpoints")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.operations.Pool.Exec(fixture.ctx, "UPDATE "+checkpoints+" SET delta_blob_id=$1, response_blob_id=$1, settings_patch_blob_id=$1 WHERE checkpoint_id=$2", checkpointBlob.BlobID, fixture.checkpointID); err != nil {
		t.Fatal(err)
	}

	providerState, err := fixture.operations.Namespace.Render("checkpoint_provider_state")
	if err != nil {
		t.Fatal(err)
	}
	accountDigest := sha256.Sum256([]byte("account"))
	if _, err := fixture.operations.Pool.Exec(fixture.ctx, "INSERT INTO "+providerState+" (checkpoint_id, ordinal, provider, endpoint_id, endpoint_account_hmac, region, endpoint_family, model_lineage, state_kind, state_blob_id, state_digest, required, immutable_fork_safe, expires_at) VALUES ($1,0,'fixture','endpoint', $2,'region','family','lineage','opaque',$3,$4,true,true,$5)", fixture.checkpointID, accountDigest[:], providerBlob.BlobID, providerBlob.Digest[:], now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Publish a normal cache entry, then move its response to a blob. This uses
	// the production cache constraints and exercises the ready-cache fence.
	cache := DefaultResponseCacheRepository(fixture.operations.Pool, fixture.operations.Namespace, fixture.operations.Keys)
	key := testCacheKey()
	key.ScopeID = fixture.scope.ID
	fill := CacheFillRequest{Key: key, OperationID: fixture.originID, Lease: time.Minute}
	if acquired, err := cache.BeginFill(fixture.ctx, fill); err != nil || acquired.Status != CacheFillAcquired {
		t.Fatalf("begin cache fill=%#v err=%v", acquired, err)
	}
	entry, err := cache.Publish(fixture.ctx, CachePublishRequest{Fill: fill, CanonicalRequestJSON: []byte(`{"model":"fixture"}`), SemanticProfileVersion: "blob-gc", CacheEpoch: "blob-gc", OriginOperationID: fixture.originID, OriginCheckpointID: fixture.checkpointID, OriginProvider: "fixture", OriginEndpointID: "endpoint", OriginResolvedModel: "model", Response: []byte(`{"response":"blob-gc"}`)})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fixture.operations.Namespace.Render("response_cache_entries")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.operations.Pool.Exec(fixture.ctx, "UPDATE "+entries+" SET response_inline_ciphertext=NULL, response_key_id=NULL, response_blob_id=$1, state='ready' WHERE cache_entry_id=$2", cacheBlob.BlobID, entry.ID); err != nil {
		t.Fatal(err)
	}

	result, err := maintenance.MarkExpiredBlobsEligible(fixture.ctx, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if result.Eligible < 1 {
		t.Fatalf("eligible result=%#v, expected the unreferenced blob", result)
	}
	for name, id := range map[string]uuid.UUID{"operation": operationBlob.BlobID, "checkpoint": checkpointBlob.BlobID, "provider": providerBlob.BlobID, "cache": cacheBlob.BlobID} {
		claims, err := maintenance.ClaimBlobDeletion(fixture.ctx, now, []uuid.UUID{id}, 1)
		if err != nil {
			t.Fatalf("claim %s: %v", name, err)
		}
		if len(claims) != 0 {
			t.Fatalf("claim %s returned retained blob: %#v", name, claims)
		}
	}
	claims, err := maintenance.ClaimBlobDeletion(fixture.ctx, now, []uuid.UUID{free.BlobID}, 1)
	if err != nil || len(claims) != 1 || claims[0].DeletionState != "deleting" {
		t.Fatalf("claim free=%#v err=%v", claims, err)
	}
	if err := maintenance.FinalizeBlobDeletion(fixture.ctx, free.BlobID); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.FinalizeBlobDeletion(fixture.ctx, free.BlobID); err != nil {
		t.Fatalf("repeat finalize was not idempotent: %v", err)
	}
	if err := maintenance.FinalizeBlobDeletion(fixture.ctx, uuid.New()); err != nil {
		t.Fatalf("missing blob finalize was not a success: %v", err)
	}
}
