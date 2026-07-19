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
	schema := pgx.Identifier{namespace.Schema}.Sanitize()
	statement := fmt.Sprintf(`DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'llmtw_runtime') THEN
    GRANT USAGE ON SCHEMA %s TO llmtw_runtime;
    GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s TO llmtw_runtime;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'llmtw_maintenance') THEN
    GRANT USAGE ON SCHEMA %s TO llmtw_maintenance;
    GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s TO llmtw_maintenance;
  END IF;
END $$;`, schema, schema, schema, schema)
	if _, err := tx.Exec(ctx, statement); err != nil {
		return fmt.Errorf("grant PostgreSQL runtime roles: %w", err)
	}
	return nil
}
