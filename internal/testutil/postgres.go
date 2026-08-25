// Package testutil holds shared integration-test helpers. The primary one is an
// ephemeral Postgres: if TEST_DATABASE_URL is set (e.g. a CI service container)
// it is used directly; otherwise a disposable postgres:16 container is started
// via testcontainers-go, which is the primary integration test mode.
package testutil

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/cobanov/punchcard/internal/repo"
)

// Postgres provisions a ready-to-use, fully-migrated Postgres and returns a
// connection pool. All resources are torn down via t.Cleanup. If no database is
// available (TEST_DATABASE_URL unset and Docker not running) the test is
// skipped rather than failed, so unit-only runs stay green locally.
func Postgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = startContainer(ctx, t)
	}

	pool, err := repo.NewPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("connect test pool: %v", err)
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

func startContainer(ctx context.Context, t *testing.T) string {
	t.Helper()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("agenttodo_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Skipf("skipping integration test: cannot start postgres container (is Docker running?): %v", err)
	}
	t.Cleanup(func() {
		termCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = container.Terminate(termCtx)
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("container connection string: %v", err)
	}
	return dsn
}
