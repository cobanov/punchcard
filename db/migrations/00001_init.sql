-- +goose Up
-- Phase 0 baseline. No domain tables yet (those arrive in Phases 1-2). We only
-- enable the citext extension here because the schema stores emails as citext
-- (case-insensitive uniqueness for users/invites), and enabling it is a
-- foundational, dependency-free step the rest of the schema builds on.
CREATE EXTENSION IF NOT EXISTS citext;

-- +goose Down
DROP EXTENSION IF EXISTS citext;
