package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestInventoryQueryPlansUseTheLatestIndex exercises the production inventory
// query at enough rows for PostgreSQL to cost an index path meaningfully.  The
// fixture deliberately spreads one configuration across many endpoint routes;
// the requested route is selective, while its endpoint account epochs still
// exercise DISTINCT ON's latest-per-account grouping.
//
// This is an index eligibility contract, not a latency or SLO measurement. It
// uses the normal planner settings and fails if the query regresses to a
// sequential scan despite the checked-in latest-per-account snapshot index.
func TestInventoryQueryPlansUseTheLatestIndex(t *testing.T) {
	ctx, namespace, pool, cleanup := providerControlIntegrationPool(t)
	t.Cleanup(cleanup)

	configDigest := sha256.Sum256([]byte("inventory-query-plan-" + uuid.NewString()))
	configs, err := namespace.Render("configuration_snapshots")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO "+configs+" (config_digest, config_version, source_digest, sanitized_config) VALUES ($1,$2,$1,'{}'::jsonb)", configDigest[:], "inventory-query-plan"); err != nil {
		t.Fatal(err)
	}

	snapshots, err := namespace.Relation("provider_inventory_snapshots")
	if err != nil {
		t.Fatal(err)
	}
	snapshotsSQL := snapshots.Sanitize()
	models, err := namespace.Relation("provider_inventory_models")
	if err != nil {
		t.Fatal(err)
	}
	modelsSQL := models.Sanitize()
	const (
		rowCount       = 10_000
		endpointCount  = 100
		accountEpochs  = 4
		targetProvider = "provider-07"
		targetEndpoint = "endpoint-47"
	)
	base := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	snapshotRows := make([][]any, 0, rowCount)
	modelRows := make([][]any, 0, rowCount)
	for i := 0; i < rowCount; i++ {
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("inventory-query-plan-%d", i)))
		provider := fmt.Sprintf("provider-%02d", i%10)
		endpoint := fmt.Sprintf("endpoint-%02d", i%endpointCount)
		account := sha256.Sum256([]byte(fmt.Sprintf("inventory-query-plan-account-%d", i%accountEpochs)))
		observed := base.Add(time.Duration(i) * time.Second)
		inventoryDigest := sha256.Sum256([]byte(id.String()))
		snapshotRows = append(snapshotRows, []any{
			id, configDigest[:], provider, endpoint, account[:], "chat", "test-region", "provider_api", observed,
			true, nil, nil, inventoryDigest[:], observed.Add(time.Hour),
		})
		capabilityDigest := sha256.Sum256([]byte("capability-" + id.String()))
		modelRows = append(modelRows, []any{
			id, "model-" + id.String(), "query-plan model", "fixture", observed, "available", capabilityDigest[:], []byte(`{}`),
		})
	}
	if _, err := pool.CopyFrom(ctx, snapshots, []string{
		"inventory_snapshot_id", "config_digest", "provider", "endpoint_id", "endpoint_account_hmac", "endpoint_family", "region", "source", "observed_at", "complete", "next_cursor_ciphertext", "next_cursor_key_id", "inventory_digest", "expires_at",
	}, pgx.CopyFromRows(snapshotRows)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.CopyFrom(ctx, models, []string{
		"inventory_snapshot_id", "provider_model_id", "display_name", "owned_by", "created_at_provider", "lifecycle_state", "capability_digest", "safe_metadata",
	}, pgx.CopyFromRows(modelRows)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM "+modelsSQL+" WHERE inventory_snapshot_id IN (SELECT inventory_snapshot_id FROM "+snapshotsSQL+" WHERE config_digest=$1)", configDigest[:])
		_, _ = pool.Exec(ctx, "DELETE FROM "+snapshotsSQL+" WHERE config_digest=$1", configDigest[:])
		_, _ = pool.Exec(ctx, "DELETE FROM "+configs+" WHERE config_digest=$1", configDigest[:])
	})
	if _, err := pool.Exec(ctx, "ANALYZE "+snapshotsSQL); err != nil {
		t.Fatal(err)
	}

	wantIndex, err := namespace.PrefixName("provider_inventory_latest_account_idx")
	if err != nil {
		t.Fatal(err)
	}
	args := []any{configDigest[:], targetProvider, targetEndpoint}
	horizonPlan := explainJSONPlan(t, ctx, pool, latestInventoryHorizonQuery(snapshots.Sanitize()), args...)
	assertPlanUsesIndex(t, horizonPlan, wantIndex)

	// The model-page query has the same selective snapshot predicate and joins
	// through the immutable snapshot identity. Its plan must retain the same
	// latest-snapshot access path as the horizon query.
	modelsPlan := explainJSONPlan(t, ctx, pool, latestInventoryModelsQuery(snapshots.Sanitize(), models.Sanitize()),
		configDigest[:], targetProvider, targetEndpoint, base.Add(time.Duration(rowCount)*time.Second), "", "", "", "", uuid.Nil, "", 2)
	assertPlanUsesIndex(t, modelsPlan, wantIndex)
}

func explainJSONPlan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) any {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(ctx, "EXPLAIN (FORMAT JSON, COSTS OFF) "+query, args...).Scan(&raw); err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	var plan any
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatalf("decode query plan: %v", err)
	}
	return plan
}

func assertPlanUsesIndex(t *testing.T, plan any, wantIndex string) {
	t.Helper()
	var walk func(any) bool
	walk = func(value any) bool {
		switch node := value.(type) {
		case map[string]any:
			if index, ok := node["Index Name"].(string); ok && index == wantIndex {
				return true
			}
			for _, child := range node {
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range node {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	if !walk(plan) {
		encoded, _ := json.Marshal(plan)
		t.Fatalf("query plan did not use %q: %s", wantIndex, encoded)
	}
}
