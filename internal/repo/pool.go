// Package repo is the ONLY layer permitted to run SQL. It owns the
// pgx connection pool, migrations, and (from Phase 2) the sqlc-backed
// repository implementations. Every task/list repository method takes the
// acting principal and joins through list_members so tenant isolation is
// structural, not conventional. A CI depguard rule forbids importing pgx or
// database/sql anywhere outside this package.
package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool creates and verifies a pgx connection pool from a DSN/URL.
func NewPool(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	// Keep a small warm pool so the first request burst after startup (or after
	// idle connections are reaped) doesn't pay the connection-establishment cost.
	cfg.MinConns = 2
	if cfg.MinConns > cfg.MaxConns {
		cfg.MinConns = cfg.MaxConns
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pgx pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
