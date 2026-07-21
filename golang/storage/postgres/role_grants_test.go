package postgres

import (
	"strings"
	"testing"
)

func TestRenderRoleGrantsUsesLeastPrivilegeRuntimeCatalog(t *testing.T) {
	namespace, err := NewNamespace("llm_worker", "private", "tenant_")
	if err != nil {
		t.Fatal(err)
	}
	sql, err := RenderRoleGrants(namespace)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(sql, "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES") {
		t.Fatal("role ACL must not grant all table privileges")
	}
	for _, required := range []string{
		`REVOKE ALL ON ALL TABLES IN SCHEMA "private" FROM llmtw_runtime;`,
		`GRANT USAGE ON SCHEMA "private" TO llmtw_runtime;`,
		`GRANT SELECT, INSERT ON TABLE "private"."tenant_conversation_checkpoints" TO llmtw_runtime;`,
		`GRANT SELECT, INSERT ON TABLE "private"."tenant_checkpoint_provider_state" TO llmtw_runtime;`,
		`GRANT SELECT, INSERT ON TABLE "private"."tenant_checkpoint_provider_affinities" TO llmtw_runtime;`,
		`GRANT UPDATE (expires_at) ON TABLE "private"."tenant_blobs" TO llmtw_runtime;`,
		`GRANT SELECT, INSERT, UPDATE ON TABLE "private"."tenant_operations" TO llmtw_runtime;`,
		`GRANT UPDATE (response_digest) ON TABLE "private"."tenant_query_executions" TO llmtw_runtime;`,
		`GRANT SELECT ON TABLE "private"."tenant_conversation_checkpoints" TO llmtw_maintenance;`,
		`GRANT INSERT, UPDATE, DELETE ON TABLE "private"."tenant_conversation_checkpoints" TO llmtw_maintenance;`,
		`GRANT USAGE ON ALL SEQUENCES IN SCHEMA "private" TO llmtw_runtime;`,
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("rendered role grants missing %q", required)
		}
	}

	// Immutable checkpoint/provider records must never acquire destructive or
	// mutable runtime privileges through a future broad-grant regression.
	for _, table := range []string{
		"tenant_conversation_checkpoints",
		"tenant_checkpoint_provider_state",
		"tenant_checkpoint_provider_affinities",
		"tenant_provider_status_events",
		"tenant_provider_inventory_snapshots",
		"tenant_provider_inventory_models",
	} {
		for _, line := range strings.Split(sql, "\n") {
			if !strings.Contains(line, "TO llmtw_runtime;") || !strings.Contains(line, table) {
				continue
			}
			if strings.Contains(line, "UPDATE") || strings.Contains(line, "DELETE") {
				t.Errorf("runtime received forbidden privilege: %s", strings.TrimSpace(line))
			}
		}
	}
}

func TestRenderRoleGrantsRejectsInvalidNamespace(t *testing.T) {
	if _, err := RenderRoleGrants(Namespace{Database: "worker", Schema: "private.bad"}); err == nil {
		t.Fatal("invalid schema was accepted")
	}
}
