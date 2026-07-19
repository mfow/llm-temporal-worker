package postgres

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mfow/llm-temporal-worker/golang/admission"
	"github.com/mfow/llm-temporal-worker/golang/pricing"
)

type responseCacheFixture struct {
	repository   ResponseCacheRepository
	operations   OperationRepository
	ctx          context.Context
	cleanup      func()
	scope        Scope
	originID     string
	checkpointID uuid.UUID
}

func newResponseCacheFixture(t *testing.T) responseCacheFixture {
	t.Helper()
	operations, ctx, cleanup := operationIntegrationRepository(t)
	t.Cleanup(cleanup)
	scope, err := operations.Scopes.Ensure(ctx, "cache-integration-tenant", "cache-integration-project")
	if err != nil {
		t.Fatal(err)
	}
	originID := "cache-origin-" + uuid.NewString()
	origin, err := operations.Begin(ctx, admission.BeginRequest{
		ID:              originID,
		ScopeKey:        "cache-integration-tenant/cache-integration-project",
		RequestDigest:   admission.Digest([]byte(originID)),
		ReservationUSD:  pricing.MustUSD("0"),
		ExpiresAt:       time.Now().UTC().Add(time.Hour),
		RequestManifest: []byte(`{"model":"fixture"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	blobs := BlobRepository{Pool: operations.Pool, Namespace: operations.Namespace, Keys: operations.Keys, NewID: UUIDv7}
	digest := sha256.Sum256([]byte(originID + "-blob"))
	blob, err := blobs.PutLocator(ctx, scope.ID, "cache-fixture", BlobMetadata{StoreID: "cache-fixture-store", Digest: digest, ByteLength: 8, MediaType: "application/json"}, []byte("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	checkpointID := uuid.New()
	publicID := sha256.Sum256([]byte(originID + "-public"))
	lineage := sha256.Sum256([]byte(originID + "-lineage"))
	settings := sha256.Sum256([]byte(originID + "-settings"))
	frontier := sha256.Sum256([]byte(originID + "-frontier"))
	checkpoints, err := operations.Namespace.Render("conversation_checkpoints")
	if err != nil {
		t.Fatal(err)
	}
	_, err = operations.Pool.Exec(ctx, "INSERT INTO "+checkpoints+" (checkpoint_id, scope_id, public_id_hmac, handle_key_id, checkpoint_kind, depth, origin_operation_id, delta_blob_id, response_blob_id, settings_patch_blob_id, canonical_lineage_digest, materialized_settings_digest, tool_frontier_digest, schema_version, compiler_epoch, expires_at) VALUES ($1,$2,$3,$4,'generation',0,$5,$6,$6,$6,$7,$8,$9,1,'cache-fixture',$10)", checkpointID, scope.ID, publicID[:], operations.Keys.Active, operationUUID(origin.Operation.ID), blob.BlobID, lineage[:], settings[:], frontier[:], time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return responseCacheFixture{repository: DefaultResponseCacheRepository(operations.Pool, operations.Namespace, operations.Keys), operations: operations, ctx: ctx, cleanup: cleanup, scope: scope, originID: origin.Operation.ID, checkpointID: checkpointID}
}

func TestResponseCacheFillLookupAndUseAccounting(t *testing.T) {
	fixture := newResponseCacheFixture(t)
	key := testCacheKey()
	key.ScopeID = fixture.scope.ID
	fill := CacheFillRequest{Key: key, OperationID: fixture.originID, Lease: time.Minute}
	acquired, err := fixture.repository.BeginFill(fixture.ctx, fill)
	if err != nil || acquired.Status != CacheFillAcquired || acquired.LeaseUntil.IsZero() {
		t.Fatalf("begin fill=%#v err=%v", acquired, err)
	}
	published, err := fixture.repository.Publish(fixture.ctx, CachePublishRequest{
		Fill:                   fill,
		CanonicalRequestJSON:   []byte(`{"model":"fixture"}`),
		SemanticProfileVersion: "profile-v1",
		CacheEpoch:             "epoch-v1",
		OriginOperationID:      fixture.originID,
		OriginCheckpointID:     fixture.checkpointID,
		OriginProvider:         "fixture",
		OriginEndpointID:       "endpoint-fixture",
		OriginResolvedModel:    "model-fixture",
		Response:               []byte(`{"output":"cached"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if published.ID == uuid.Nil || published.UseCount != 1 || string(published.Response.Ciphertext) == `{"output":"cached"}` {
		t.Fatalf("published entry=%#v", published)
	}
	consumerID := "cache-consumer-" + uuid.NewString()
	if _, err := fixture.operations.Begin(fixture.ctx, admission.BeginRequest{ID: consumerID, ScopeKey: "cache-integration-tenant/cache-integration-project", RequestDigest: admission.Digest([]byte(consumerID)), ReservationUSD: pricing.MustUSD("0"), ExpiresAt: time.Now().UTC().Add(time.Hour), RequestManifest: []byte(`{"model":"fixture"}`)}); err != nil {
		t.Fatal(err)
	}
	hit, err := fixture.repository.Lookup(fixture.ctx, CacheLookupRequest{Key: key, OperationID: consumerID, MaxAge: time.Hour})
	if err != nil || !hit.Hit || string(hit.Response) != `{"output":"cached"}` {
		t.Fatalf("cache hit=%#v err=%v", hit, err)
	}
	// A Temporal retry reuses the same logical operation and must not inflate
	// use_count or create a second response_cache_uses row.
	retry, err := fixture.repository.Lookup(fixture.ctx, CacheLookupRequest{Key: key, OperationID: consumerID, MaxAge: time.Hour})
	if err != nil || !retry.Hit || string(retry.Response) != `{"output":"cached"}` {
		t.Fatalf("cache retry=%#v err=%v", retry, err)
	}
	entries, err := fixture.operations.Namespace.Render("response_cache_entries")
	if err != nil {
		t.Fatal(err)
	}
	var useCount int
	if err := fixture.operations.Pool.QueryRow(fixture.ctx, "SELECT use_count FROM "+entries+" WHERE cache_entry_id=$1", published.ID).Scan(&useCount); err != nil {
		t.Fatal(err)
	}
	if useCount != 2 {
		t.Fatalf("use_count=%d, want origin plus one logical hit", useCount)
	}
	wrongRoute := key
	wrongRoute.RouteIdentityHMAC = sha256.Sum256([]byte("different-route"))
	miss, err := fixture.repository.Lookup(fixture.ctx, CacheLookupRequest{Key: wrongRoute, OperationID: "cache-route-miss-" + uuid.NewString(), MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if miss.Hit {
		t.Fatal("route-isolated cache entry crossed route identity")
	}
}

func TestResponseCacheFillLeaseBusyAndTakeover(t *testing.T) {
	fixture := newResponseCacheFixture(t)
	key := testCacheKey()
	key.ScopeID = fixture.scope.ID
	first := CacheFillRequest{Key: key, OperationID: fixture.originID, Lease: time.Minute}
	if result, err := fixture.repository.BeginFill(fixture.ctx, first); err != nil || result.Status != CacheFillAcquired {
		t.Fatalf("first fill=%#v err=%v", result, err)
	}
	secondID := "cache-fill-contender-" + uuid.NewString()
	if _, err := fixture.operations.Begin(fixture.ctx, admission.BeginRequest{ID: secondID, ScopeKey: "cache-integration-tenant/cache-integration-project", RequestDigest: admission.Digest([]byte(secondID)), ReservationUSD: pricing.MustUSD("0"), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	second := CacheFillRequest{Key: key, OperationID: secondID, Lease: time.Minute}
	if result, err := fixture.repository.BeginFill(fixture.ctx, second); err != nil || result.Status != CacheFillBusy {
		t.Fatalf("busy fill=%#v err=%v", result, err)
	}
	// A different route identity is an independent fill, even when the
	// semantic fingerprint and variant are identical.
	thirdID := "cache-fill-other-route-" + uuid.NewString()
	if _, err := fixture.operations.Begin(fixture.ctx, admission.BeginRequest{ID: thirdID, ScopeKey: "cache-integration-tenant/cache-integration-project", RequestDigest: admission.Digest([]byte(thirdID)), ReservationUSD: pricing.MustUSD("0"), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	otherRoute := key
	otherRoute.RouteIdentityHMAC = sha256.Sum256([]byte("other-route"))
	otherRouteFill := CacheFillRequest{Key: otherRoute, OperationID: thirdID, Lease: time.Minute}
	if result, err := fixture.repository.BeginFill(fixture.ctx, otherRouteFill); err != nil || result.Status != CacheFillAcquired {
		t.Fatalf("other route fill=%#v err=%v", result, err)
	}
	if err := fixture.repository.FailFill(fixture.ctx, otherRouteFill); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.FailFill(fixture.ctx, first); err != nil {
		t.Fatal(err)
	}
	if result, err := fixture.repository.BeginFill(fixture.ctx, second); err != nil || result.Status != CacheFillAcquired {
		t.Fatalf("takeover fill=%#v err=%v", result, err)
	}
}
