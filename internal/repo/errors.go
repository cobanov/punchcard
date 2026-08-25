package repo

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNoRows is returned by single-row queries when nothing matches. Re-exported
// so the service layer can detect "not found" without importing pgx.
var ErrNoRows = pgx.ErrNoRows

// IsNotFound reports whether err indicates no rows were found.
func IsNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// IsUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505).
func IsUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == "23505"
	}
	return false
}

// IsForeignKeyViolation reports whether err is a FK violation (23503).
func IsForeignKeyViolation(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == "23503"
	}
	return false
}
