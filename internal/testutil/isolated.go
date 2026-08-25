package testutil

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cobanov/punchcard/internal/repo"
)

// IsolatedPostgres provisions a private, fully-migrated database even when
// TEST_DATABASE_URL points at a server shared by concurrently-running package
// test binaries. Tests that mutate global state (e.g. deleting outbox events
// to move the retention horizon) must use this instead of Postgres, or they
// race with other packages' tests. The testcontainers path is already
// per-test isolated, so it is returned as-is.
func IsolatedPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		return Postgres(t)
	}

	ctx := context.Background()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	name := fmt.Sprintf("agenttodo_iso_%d", time.Now().UnixNano())

	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create isolated database: %v", err)
	}
	t.Cleanup(func() {
		// Runs after the pool's Close (LIFO); FORCE covers stragglers.
		dropCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = admin.Exec(dropCtx, "DROP DATABASE "+name+" WITH (FORCE)")
		_ = admin.Close(dropCtx)
	})

	u.Path = "/" + name
	pool, err := repo.NewPool(ctx, u.String(), 5)
	if err != nil {
		t.Fatalf("connect isolated pool: %v", err)
	}
	t.Cleanup(pool.Close)

	migrator, err := repo.NewMigrator(pool)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}
	t.Cleanup(func() { _ = migrator.Close() })
	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return pool
}
