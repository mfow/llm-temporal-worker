package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ContractVersion = "worker_state_v1"

//go:embed schema/000001_worker_state.sql
var schemaFiles embed.FS

func migrationTemplate() ([]byte, error) {
	return schemaFiles.ReadFile("schema/000001_worker_state.sql")
}

func RenderMigration(namespace Namespace) (string, error) {
	if err := namespace.Validate(); err != nil {
		return "", err
	}
	template, err := migrationTemplate()
	if err != nil {
		return "", fmt.Errorf("read PostgreSQL migration: %w", err)
	}
	// The only interpolated values have passed the strict lower-case
	// identifier checks above. Everything else remains a static migration.
	sql := strings.ReplaceAll(string(template), "__SCHEMA__", namespace.Schema)
	sql = strings.ReplaceAll(sql, "__PREFIX__", namespace.TablePrefix)
	if strings.Contains(sql, "search_path") || strings.Contains(sql, "__SCHEMA__") || strings.Contains(sql, "__PREFIX__") {
		return "", fmt.Errorf("rendered migration contains an unsafe placeholder or search_path")
	}
	return sql, nil
}

// Verify checks the immutable schema contract without mutating PostgreSQL.
func Verify(ctx context.Context, pool *pgxpool.Pool, namespace Namespace) error {
	if pool == nil {
		return fmt.Errorf("postgres pool is nil")
	}
	if err := namespace.Validate(); err != nil {
		return err
	}
	if err := verifyDatabase(ctx, pool, namespace); err != nil {
		return err
	}
	relation, err := namespace.Render("schema_contract")
	if err != nil {
		return err
	}
	migration, err := RenderMigration(namespace)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(migration))
	var version string
	var stored []byte
	if err := pool.QueryRow(ctx, "SELECT contract_version, migration_digest FROM "+relation+" WHERE contract_name = $1", ContractVersion).Scan(&version, &stored); err != nil {
		return fmt.Errorf("verify PostgreSQL schema contract: %w", err)
	}
	if version != ContractVersion {
		return fmt.Errorf("PostgreSQL schema contract version %q is not %q", version, ContractVersion)
	}
	if string(stored) != string(digest[:]) {
		return fmt.Errorf("PostgreSQL schema contract digest does not match %s", ContractVersion)
	}
	return nil
}

// Install applies the pinned migration exactly once. Startup code should call
// Verify; this mutating function belongs only to an explicit provisioning or
// migration step.
func Install(ctx context.Context, pool *pgxpool.Pool, namespace Namespace) error {
	if pool == nil {
		return fmt.Errorf("postgres pool is nil")
	}
	if err := namespace.Validate(); err != nil {
		return err
	}
	if err := verifyDatabase(ctx, pool, namespace); err != nil {
		return err
	}
	sql, err := RenderMigration(namespace)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(sql))
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin PostgreSQL schema install: %w", err)
	}
	defer tx.Rollback(ctx)

	relation, err := namespace.Render("schema_contract")
	if err != nil {
		return err
	}
	var existing *string
	if err := tx.QueryRow(ctx, "SELECT to_regclass($1)::text", relation).Scan(&existing); err != nil {
		return fmt.Errorf("check PostgreSQL schema contract: %w", err)
	}
	if existing != nil && *existing != "" {
		var version string
		var stored []byte
		err := tx.QueryRow(ctx, "SELECT contract_version, migration_digest FROM "+relation+" WHERE contract_name = $1", ContractVersion).Scan(&version, &stored)
		if err != nil {
			return fmt.Errorf("read PostgreSQL schema contract: %w", err)
		}
		if version != ContractVersion || string(stored) != string(digest[:]) {
			return fmt.Errorf("PostgreSQL schema contract does not match %s", ContractVersion)
		}
		// Role grants are deliberately reconciled on every idempotent install.
		// This repairs a namespace installed by an older worker version without
		// mutating any schema objects or changing the contract digest.
		if err := grantRuntimeRoles(ctx, tx, namespace); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL TIME ZONE 'UTC'"); err != nil {
		return fmt.Errorf("set PostgreSQL schema timezone: %w", err)
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		return fmt.Errorf("apply PostgreSQL schema migration: %w", err)
	}
	if err := renameGeneratedConstraints(ctx, tx, namespace); err != nil {
		return err
	}
	if err := grantRuntimeRoles(ctx, tx, namespace); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO "+relation+" (contract_name, contract_version, migration_digest) VALUES ($1, $2, $3)", ContractVersion, ContractVersion, digest[:]); err != nil {
		return fmt.Errorf("record PostgreSQL schema contract: %w", err)
	}
	return tx.Commit(ctx)
}

func verifyDatabase(ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, namespace Namespace) error {
	var current string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&current); err != nil {
		return fmt.Errorf("check PostgreSQL database: %w", err)
	}
	if current != namespace.Database {
		return fmt.Errorf("connected PostgreSQL database %q, expected %q", current, namespace.Database)
	}
	return nil
}

func renameGeneratedConstraints(ctx context.Context, tx pgx.Tx, namespace Namespace) error {
	rows, err := tx.Query(ctx, `SELECT c.oid, c.conname, c.contype, t.relname
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = $1
ORDER BY t.relname, c.conname`, namespace.Schema)
	if err != nil {
		return fmt.Errorf("inspect PostgreSQL constraints: %w", err)
	}
	defer rows.Close()
	type constraint struct{ name, kind, table string }
	var constraints []constraint
	for rows.Next() {
		var oid uint32
		var item constraint
		if err := rows.Scan(&oid, &item.name, &item.kind, &item.table); err != nil {
			return err
		}
		constraints = append(constraints, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Slice(constraints, func(i, j int) bool {
		if constraints[i].table != constraints[j].table {
			return constraints[i].table < constraints[j].table
		}
		return constraints[i].name < constraints[j].name
	})
	for index, item := range constraints {
		if strings.HasPrefix(item.name, namespace.TablePrefix+"c_") {
			continue
		}
		name := fmt.Sprintf("%sc_%s_%02d", namespace.TablePrefix, constraintKind(item.kind), index+1)
		if len(name) > MaxIdentifierBytes {
			return fmt.Errorf("generated PostgreSQL constraint name %q exceeds 63 bytes", name)
		}
		tableID := pgx.Identifier{namespace.Schema, item.table}.Sanitize()
		oldID := pgx.Identifier{item.name}.Sanitize()
		newID := pgx.Identifier{name}.Sanitize()
		if _, err := tx.Exec(ctx, "ALTER TABLE "+tableID+" RENAME CONSTRAINT "+oldID+" TO "+newID); err != nil {
			return fmt.Errorf("rename PostgreSQL constraint %q: %w", item.name, err)
		}
	}
	return nil
}

func constraintKind(kind string) string {
	switch kind {
	case "p":
		return "pk"
	case "u":
		return "uq"
	case "f":
		return "fk"
	case "c":
		return "ck"
	default:
		return "co"
	}
}

func grantRuntimeRoles(ctx context.Context, tx pgx.Tx, namespace Namespace) error {
	statement, err := RenderRoleGrants(namespace)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, statement); err != nil {
		return fmt.Errorf("grant PostgreSQL runtime roles: %w", err)
	}
	return nil
}

// RenderRoleGrants renders the role ACL reconciliation applied by Install.
//
// The runtime role is intentionally allow-listed. In particular, immutable
// checkpoint/blob/provider/control records are append-only for the worker;
// retention and other destructive operations belong to llmtw_maintenance.
// The schema owner remains the only role that can create or alter objects.
func RenderRoleGrants(namespace Namespace) (string, error) {
	if err := namespace.Validate(); err != nil {
		return "", err
	}
	schema := pgx.Identifier{namespace.Schema}.Sanitize()

	// Keep this catalog in logical-name form so every physical relation still
	// passes through Namespace.Render and receives the configured prefix.
	allTables := []string{
		"schema_contract", "scopes", "configuration_snapshots", "blobs",
		"operations", "operation_attempts", "conversation_checkpoints",
		"checkpoint_provider_state", "checkpoint_provider_affinities",
		"response_cache_entries", "response_cache_uses", "response_cache_fills",
		"budget_policies", "budget_windows", "budget_redis_generations",
		"budget_journal_events", "budget_buckets", "operation_budget_reservations",
		"price_catalogs", "price_entries", "provider_status_events",
		"provider_route_status", "provider_inventory_snapshots",
		"provider_inventory_models", "maintenance_outbox", "query_executions",
	}
	runtime := []roleGrant{
		{table: "schema_contract", privileges: "SELECT"},
		{table: "scopes", privileges: "SELECT, INSERT"},
		{table: "scopes", privileges: "UPDATE (deleted_at)"},
		{table: "configuration_snapshots", privileges: "SELECT, INSERT"},
		{table: "blobs", privileges: "SELECT, INSERT"},
		// Blob rows are immutable except for extending retention. Restrict the
		// runtime update privilege to that one column.
		{table: "blobs", privileges: "UPDATE (expires_at)"},
		{table: "operations", privileges: "SELECT, INSERT, UPDATE"},
		{table: "operation_attempts", privileges: "SELECT, INSERT"},
		{table: "conversation_checkpoints", privileges: "SELECT, INSERT"},
		{table: "checkpoint_provider_state", privileges: "SELECT, INSERT"},
		{table: "checkpoint_provider_affinities", privileges: "SELECT, INSERT"},
		{table: "response_cache_entries", privileges: "SELECT, INSERT, UPDATE"},
		{table: "response_cache_uses", privileges: "SELECT, INSERT"},
		{table: "response_cache_fills", privileges: "SELECT, INSERT, UPDATE"},
		{table: "budget_policies", privileges: "SELECT"},
		{table: "budget_windows", privileges: "SELECT"},
		{table: "budget_redis_generations", privileges: "SELECT, INSERT, UPDATE"},
		// Append statements contain no SELECT. PostgreSQL nevertheless requires
		// column SELECT privileges for RETURNING and values referenced by an
		// ON CONFLICT/UPDATE expression, so grant only those dependencies.
		{table: "budget_journal_events", privileges: "SELECT (journal_id, event_id, operation_id, window_id, reservation_revision), INSERT, UPDATE (event_id)"},
		{table: "budget_buckets", privileges: "SELECT (reserved_cost_usd, accounted_cost_usd, last_journal_id), INSERT, UPDATE"},
		{table: "operation_budget_reservations", privileges: "SELECT (operation_id, window_id, reserved_cost_usd), INSERT, UPDATE"},
		{table: "price_catalogs", privileges: "SELECT"},
		{table: "price_entries", privileges: "SELECT"},
		{table: "provider_status_events", privileges: "SELECT, INSERT"},
		{table: "provider_route_status", privileges: "SELECT, INSERT, UPDATE"},
		{table: "provider_inventory_snapshots", privileges: "SELECT, INSERT"},
		{table: "provider_inventory_models", privileges: "SELECT, INSERT"},
		{table: "maintenance_outbox", privileges: "SELECT, INSERT, UPDATE"},
		{table: "query_executions", privileges: "SELECT, INSERT"},
		// The response digest is finalized after the bounded response JSON has
		// been canonicalized; no other query-execution column is mutable.
		{table: "query_executions", privileges: "UPDATE (response_digest)"},
	}

	var b strings.Builder
	fmt.Fprintf(&b, "DO $$\nBEGIN\n")
	fmt.Fprintf(&b, "  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'llmtw_runtime') THEN\n")
	fmt.Fprintf(&b, "    REVOKE ALL ON ALL TABLES IN SCHEMA %s FROM llmtw_runtime;\n", schema)
	fmt.Fprintf(&b, "    REVOKE ALL ON ALL SEQUENCES IN SCHEMA %s FROM llmtw_runtime;\n", schema)
	fmt.Fprintf(&b, "    GRANT USAGE ON SCHEMA %s TO llmtw_runtime;\n", schema)
	for _, grant := range runtime {
		if err := appendRoleGrant(&b, namespace, grant, "llmtw_runtime"); err != nil {
			return "", err
		}
	}
	// Identity columns are used by append-only event tables. USAGE is enough
	// for INSERT and does not allow a worker to alter or restart a sequence.
	fmt.Fprintf(&b, "    GRANT USAGE ON ALL SEQUENCES IN SCHEMA %s TO llmtw_runtime;\n", schema)
	fmt.Fprintf(&b, "  END IF;\n")

	fmt.Fprintf(&b, "  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'llmtw_maintenance') THEN\n")
	fmt.Fprintf(&b, "    REVOKE ALL ON ALL TABLES IN SCHEMA %s FROM llmtw_maintenance;\n", schema)
	fmt.Fprintf(&b, "    REVOKE ALL ON ALL SEQUENCES IN SCHEMA %s FROM llmtw_maintenance;\n", schema)
	fmt.Fprintf(&b, "    GRANT USAGE ON SCHEMA %s TO llmtw_maintenance;\n", schema)
	for _, table := range allTables {
		if err := appendRoleGrant(&b, namespace, roleGrant{table: table, privileges: "SELECT"}, "llmtw_maintenance"); err != nil {
			return "", err
		}
	}
	for _, table := range allTables {
		if table == "schema_contract" {
			continue
		}
		if err := appendRoleGrant(&b, namespace, roleGrant{table: table, privileges: "INSERT, UPDATE, DELETE"}, "llmtw_maintenance"); err != nil {
			return "", err
		}
	}
	fmt.Fprintf(&b, "    GRANT USAGE ON ALL SEQUENCES IN SCHEMA %s TO llmtw_maintenance;\n", schema)
	fmt.Fprintf(&b, "  END IF;\nEND $$;\n")
	return b.String(), nil
}

type roleGrant struct {
	table      string
	privileges string
}

func appendRoleGrant(b *strings.Builder, namespace Namespace, grant roleGrant, role string) error {
	relation, err := namespace.Render(grant.table)
	if err != nil {
		return err
	}
	fmt.Fprintf(b, "    GRANT %s ON TABLE %s TO %s;\n", grant.privileges, relation, role)
	return nil
}
