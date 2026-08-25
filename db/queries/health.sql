-- name: Ping :one
-- Trivial connectivity probe so the sqlc pipeline is exercised end-to-end in
-- Phase 0. Real domain queries (lists, tasks, ...) arrive in Phase 2.
SELECT 1::int AS ok;
