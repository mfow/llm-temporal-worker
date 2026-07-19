package postgres

import (
	"strings"
	"testing"
)

func TestNamespaceValidationMatrix(t *testing.T) {
	valid := []struct{ database, schema, prefix string }{
		{"llm_worker", "llm_worker", ""},
		{"worker_1", "private_2", "tenant_"},
		{"a", "a", "a_"},
	}
	for _, tc := range valid {
		if _, err := NewNamespace(tc.database, tc.schema, tc.prefix); err != nil {
			t.Errorf("valid namespace %#v rejected: %v", tc, err)
		}
	}
	invalid := []struct{ database, schema, prefix string }{
		{"LLM_WORKER", "llm_worker", ""}, {"worker-db", "llm_worker", ""},
		{"worker", "llm_worker.public", ""}, {"worker", "llm_worker", "Tenant_"},
		{"worker", "llm_worker", "x"}, {"worker", "llm_worker", strings.Repeat("x", 24) + "_"},
		{"worker", "llm_worker", "worker;drop_"}, {"worker", "llm_worker", "worker\n_"},
	}
	for _, tc := range invalid {
		if _, err := NewNamespace(tc.database, tc.schema, tc.prefix); err == nil {
			t.Errorf("invalid namespace %#v accepted", tc)
		}
	}
}

func TestNamespaceIdentifiersAreQuotedAndBounded(t *testing.T) {
	ns, err := NewNamespace("llm_worker", "private", "tenant_")
	if err != nil {
		t.Fatal(err)
	}
	id, err := ns.Relation("operations")
	if err != nil {
		t.Fatal(err)
	}
	if got := id.Sanitize(); got != `"private"."tenant_operations"` {
		t.Fatalf("unexpected relation identifier %q", got)
	}
	if _, err := ns.Relation(strings.Repeat("a", 64)); err == nil {
		t.Fatal("overlength relation was accepted")
	}
	if _, err := ns.Relation("operations;drop"); err == nil {
		t.Fatal("injected relation was accepted")
	}
}

func TestMigrationTemplateContract(t *testing.T) {
	data, err := schemaFiles.ReadFile("schema/000001_worker_state.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, expected := range []string{
		"CREATE SCHEMA __SCHEMA__",
		"__SCHEMA__.__PREFIX__operations",
		"__SCHEMA__.__PREFIX__response_cache_entries",
		"__SCHEMA__.__PREFIX__budget_buckets",
		"__SCHEMA__.__PREFIX__query_executions",
		"CREATE INDEX __PREFIX__operations_completed_brin_idx",
		"fillfactor = 80",
		"numeric(38,18)",
	} {
		if !strings.Contains(sql, expected) {
			t.Errorf("migration missing %q", expected)
		}
	}
	if strings.Contains(sql, "WITH inserted") || strings.Contains(sql, "search_path") {
		t.Fatal("migration contains runtime DML or search_path")
	}
	ns, err := NewNamespace("llm_worker", "private", "tenant_")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderMigration(ns)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, "__SCHEMA__") || strings.Contains(rendered, "__PREFIX__") {
		t.Fatal("rendered migration retained a placeholder")
	}
	if !strings.Contains(rendered, `CREATE TABLE private.tenant_operations`) {
		t.Fatal("rendered migration did not apply namespace")
	}
}
