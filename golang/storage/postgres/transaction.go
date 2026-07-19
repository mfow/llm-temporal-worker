package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TxFunc is deliberately passed a pgx.Tx rather than a pool so repository
// methods cannot accidentally escape the transaction for blob/provider I/O.
type TxFunc func(context.Context, pgx.Tx) error

// WithTransaction owns begin/commit/rollback and preserves synchronous commit
// for durable state. A rollback is attempted on every non-committed path;
// rollback errors are intentionally not allowed to mask the original error.
func WithTransaction(ctx context.Context, pool *pgxpool.Pool, fn TxFunc) error {
	if ctx == nil {
		return errors.New("PostgreSQL transaction context is nil")
	}
	if pool == nil {
		return errors.New("PostgreSQL transaction pool is nil")
	}
	if fn == nil {
		return errors.New("PostgreSQL transaction callback is nil")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite, DeferrableMode: pgx.NotDeferrable})
	if err != nil {
		return redactPostgresError(fmt.Errorf("begin PostgreSQL transaction: %w", err))
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	if _, err := tx.Exec(ctx, "SET LOCAL TIME ZONE 'UTC'"); err != nil {
		return redactPostgresError(fmt.Errorf("set PostgreSQL transaction timezone: %w", err))
	}
	if _, err := tx.Exec(ctx, "SET LOCAL synchronous_commit = 'on'"); err != nil {
		return redactPostgresError(fmt.Errorf("set PostgreSQL transaction durability: %w", err))
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return redactPostgresError(fmt.Errorf("commit PostgreSQL transaction: %w", err))
	}
	committed = true
	return nil
}
