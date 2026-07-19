package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Integration tests are enabled by the PostgreSQL service in CI. Local and
// offline test runs remain deterministic and skip when no address is set.
func TestPostgresIntegrationConfiguration(t *testing.T) {
	addr := os.Getenv("LLMTW_POSTGRES_ADDR")
	if addr == "" {
		t.Skip("LLMTW_POSTGRES_ADDR is not configured; set it for PostgreSQL integration tests")
	}
	ns, err := NewNamespace(
		valueOr("LLMTW_POSTGRES_DATABASE", "llm_worker"),
		valueOr("LLMTW_POSTGRES_SCHEMA", "llm_worker"),
		os.Getenv("LLMTW_POSTGRES_TABLE_PREFIX"),
	)
	if err != nil {
		t.Fatal(err)
	}
	password := valueOr("LLMTW_POSTGRES_PASSWORD", "llmtw")
	user := valueOr("LLMTW_POSTGRES_USER", "llmtw")
	dsn := fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable", user, password, addr, ns.Database)
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.MaxConns = 4
	poolConfig.MinConns = 1
	poolConfig.MaxConnLifetime = 2 * time.Minute
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Install(ctx, pool, ns); err != nil {
		t.Fatalf("clean install: %v", err)
	}
	if err := Install(ctx, pool, ns); err != nil {
		t.Fatalf("idempotent install: %v", err)
	}
	if err := Verify(ctx, pool, ns); err != nil {
		t.Fatalf("contract verification: %v", err)
	}
	var tableCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relkind = 'r'`, ns.Schema).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount < 20 {
		t.Fatalf("schema contains %d tables, want at least 20", tableCount)
	}
}

func valueOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
