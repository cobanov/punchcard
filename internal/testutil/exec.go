package testutil

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Exec runs raw SQL against the test database. Some tests need setup no
// repository query should ever expose (e.g. deleting outbox events to simulate
// retention); testutil is the sanctioned depguard exception for direct access.
func Exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// Query runs a read for a test. Same exception as Exec: tests that must see
// the database directly (retention horizons, rows no API exposes) go through
// here so nothing else has to.
func Query(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) pgx.Rows {
	t.Helper()
	rows, err := pool.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	return rows
}

func QueryRow(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) pgx.Row {
	t.Helper()
	return pool.QueryRow(context.Background(), sql, args...)
}

// Pool aliases the pgx pool type so a caller can hold a reference (e.g. a
// field on a test harness struct) without importing pgx itself. depguard's
// sql-confined-to-repo rule confines that import to internal/repo and this
// package; a struct field spelled *pgxpool.Pool anywhere else would need the
// same import and fail the same rule it is naming this alias to avoid.
type Pool = pgxpool.Pool
