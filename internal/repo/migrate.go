package repo

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	appdb "github.com/cobanov/punchcard/db"
)

// Migrator applies the embedded goose migrations. It always runs under a
// Postgres session-level advisory lock so concurrent starts on a shared
// database cannot race. It is safe to construct once at startup
// and reuse for readiness checks.
type Migrator struct {
	provider *goose.Provider
	sqlDB    *sql.DB
}

// NewMigrator builds a Migrator over the given pool. The caller owns Close.
func NewMigrator(pool *pgxpool.Pool) (*Migrator, error) {
	sub, err := fs.Sub(appdb.Migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("sub-fs migrations: %w", err)
	}

	sqlDB := stdlib.OpenDBFromPool(pool)

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("create advisory-lock session locker: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub, goose.WithSessionLocker(locker))
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("create goose provider: %w", err)
	}

	return &Migrator{provider: provider, sqlDB: sqlDB}, nil
}

// Up applies all pending migrations.
func (m *Migrator) Up(ctx context.Context) error {
	if _, err := m.provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// Down rolls back the most recent migration.
func (m *Migrator) Down(ctx context.Context) error {
	if _, err := m.provider.Down(ctx); err != nil {
		return fmt.Errorf("roll back migration: %w", err)
	}
	return nil
}

// HasPending reports whether migrations remain to be applied. Used by /readyz.
func (m *Migrator) HasPending(ctx context.Context) (bool, error) {
	pending, err := m.provider.HasPending(ctx)
	if err != nil {
		return false, fmt.Errorf("check pending migrations: %w", err)
	}
	return pending, nil
}

// Close releases the sql.DB handle that backs the migrator. It does not close
// the underlying pgx pool.
func (m *Migrator) Close() error { return m.sqlDB.Close() }
