package postgres

import (
	"strings"
	"testing"
)

func TestMigrationIndexesRemainExplicit(t *testing.T) {
	data, err := schemaFiles.ReadFile("schema/000001_worker_state.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, index := range []string{
		"operations_completed_brin_idx",
		"budget_journal_time_brin_idx",
		"provider_status_event_brin_idx",
		"response_cache_reusable_key_uidx",
		"operations_provider_operation_uidx",
		"query_executions_unknown_cost_idx",
	} {
		if !strings.Contains(sql, "CREATE ") || !strings.Contains(sql, index) {
			t.Errorf("migration missing index %q", index)
		}
	}
	if strings.Count(sql, "CREATE INDEX ") < 20 {
		t.Fatalf("migration contains too few indexes: %d", strings.Count(sql, "CREATE INDEX "))
	}
	if !strings.Contains(sql, "INCLUDE (") || !strings.Contains(sql, "WHERE state IN") {
		t.Fatal("covering or partial index contract missing")
	}
}
