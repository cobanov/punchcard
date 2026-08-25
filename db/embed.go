// Package db embeds the goose SQL migrations so they travel inside the single
// static binary (distribution is one binary). The .sql files under
// migrations/ are the source of truth for the schema and are also read by sqlc.
package db

import "embed"

// Migrations holds every goose migration, embedded at build time. Consumed by
// internal/repo.NewMigrator.
//
//go:embed migrations/*.sql
var Migrations embed.FS
