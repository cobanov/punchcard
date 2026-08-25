package repo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cobanov/punchcard/internal/repo/db"
)

// Store is the repository facade. It embeds the sqlc-generated *db.Queries so
// callers in the service layer can invoke type-safe queries without importing
// pgx, and adds transaction support. This is the only type that hands out DB
// access; the depguard rule keeps pgx/database/sql imports inside this package.
type Store struct {
	pool *pgxpool.Pool
	*db.Queries
}

// NewStore builds a Store over the pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, Queries: db.New(pool)}
}

// WithTx runs fn inside a transaction, committing on success and rolling back
// on error. The callback receives a *db.Queries bound to the transaction.
func (s *Store) WithTx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(s.Queries.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
