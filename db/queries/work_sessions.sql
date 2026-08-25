-- name: StartWorkSession :one
INSERT INTO work_sessions (id, project_id, user_id, note, started_at, source)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- Closes whichever session is currently open for this user. Called in the same
-- transaction as StartWorkSession so a "start" that replaces a running timer is
-- atomic: there is never an instant with two open sessions, which is what the
-- one_open_session_per_user index would reject anyway.
-- name: StopOpenSessionForUser :many
UPDATE work_sessions
SET ended_at = GREATEST(sqlc.arg(at)::timestamptz, started_at + interval '1 second'),
    updated_at = now(),
    sync_state = 'pending',
    sync_next_at = now(),
    sync_attempts = 0,
    sync_error = NULL
WHERE user_id = sqlc.arg(user_id) AND ended_at IS NULL AND deleted_at IS NULL
RETURNING *;

-- name: StopWorkSession :one
UPDATE work_sessions
SET ended_at = GREATEST(sqlc.arg(at)::timestamptz, started_at + interval '1 second'),
    updated_at = now(),
    sync_state = 'pending',
    sync_next_at = now(),
    sync_attempts = 0,
    sync_error = NULL
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id)
  AND ended_at IS NULL AND deleted_at IS NULL
RETURNING *;

-- name: GetWorkSession :one
SELECT * FROM work_sessions
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: GetOpenWorkSession :one
SELECT * FROM work_sessions
WHERE user_id = $1 AND ended_at IS NULL AND deleted_at IS NULL;

-- name: ListWorkSessions :many
SELECT * FROM work_sessions
WHERE user_id = sqlc.arg(user_id)
  AND deleted_at IS NULL
  AND started_at < sqlc.arg(to_ts)::timestamptz
  AND (ended_at IS NULL OR ended_at > sqlc.arg(from_ts)::timestamptz)
  AND (sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id)::uuid)
ORDER BY started_at DESC;

-- name: UpdateWorkSession :one
UPDATE work_sessions SET
    project_id = coalesce(sqlc.narg(project_id), project_id),
    note       = coalesce(sqlc.narg(note), note),
    started_at = coalesce(sqlc.narg(started_at), started_at),
    ended_at   = CASE WHEN sqlc.arg(set_ended)::boolean THEN sqlc.narg(ended_at)
                      ELSE ended_at END,
    updated_at = now()
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteWorkSession :execrows
UPDATE work_sessions SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- Which session covers an instant. Ranges cannot overlap, so this returns at
-- most one row.
-- name: SessionCovering :one
SELECT * FROM work_sessions
WHERE user_id = sqlc.arg(user_id)
  AND deleted_at IS NULL
  AND ended_at IS NOT NULL
  AND started_at <= sqlc.arg(at)::timestamptz
  AND ended_at >= sqlc.arg(at)::timestamptz
LIMIT 1;

-- name: MarkSessionSyncPending :exec
UPDATE work_sessions
SET sync_state = 'pending', sync_next_at = now(), sync_attempts = 0, sync_error = NULL,
    updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: MarkSessionsSyncPendingSince :execrows
UPDATE work_sessions
SET sync_state = 'pending', sync_next_at = now()
WHERE ended_at IS NOT NULL
  AND deleted_at IS NULL
  AND ended_at > sqlc.arg(since)::timestamptz
  AND sync_state IN ('ok', 'error');

-- name: ClaimPendingSyncSessions :many
SELECT * FROM work_sessions
WHERE sync_state = 'pending'
  AND deleted_at IS NULL
  AND ended_at IS NOT NULL
  AND (sync_next_at IS NULL OR sync_next_at <= sqlc.arg(now)::timestamptz)
ORDER BY sync_next_at ASC NULLS FIRST
LIMIT sqlc.arg(lim)
FOR UPDATE SKIP LOCKED;

-- name: SetSessionSyncOK :exec
UPDATE work_sessions
SET sync_state = 'ok', sync_attempts = 0, sync_next_at = NULL, sync_error = NULL
WHERE id = $1;

-- name: SetSessionSyncRetry :exec
UPDATE work_sessions
SET sync_state = 'pending',
    sync_attempts = sync_attempts + 1,
    sync_next_at = sqlc.arg(next_at),
    sync_error = sqlc.arg(err)
WHERE id = sqlc.arg(id);

-- name: SetSessionSyncFailed :exec
UPDATE work_sessions
SET sync_state = 'error',
    sync_attempts = sync_attempts + 1,
    sync_next_at = NULL,
    sync_error = sqlc.arg(err)
WHERE id = sqlc.arg(id);

-- name: SetSessionSyncSkipped :execrows
UPDATE work_sessions
SET sync_state = 'skipped', sync_next_at = NULL, sync_error = sqlc.arg(err)
WHERE user_id = sqlc.arg(user_id) AND sync_state = 'pending';

-- Reports ------------------------------------------------------------------

-- name: SummaryByProject :many
SELECT
    p.id            AS project_id,
    p.name          AS project_name,
    p.client        AS client,
    p.currency      AS currency,
    p.billable      AS billable,
    p.hourly_rate_cents AS hourly_rate_cents,
    sum(extract(epoch FROM (
        least(ws.ended_at, sqlc.arg(to_ts)::timestamptz)
      - greatest(ws.started_at, sqlc.arg(from_ts)::timestamptz)
    )))::bigint AS seconds
FROM work_sessions ws
JOIN projects p ON p.id = ws.project_id
WHERE ws.user_id = sqlc.arg(user_id)
  AND ws.deleted_at IS NULL
  AND ws.ended_at IS NOT NULL
  AND ws.started_at < sqlc.arg(to_ts)::timestamptz
  AND ws.ended_at > sqlc.arg(from_ts)::timestamptz
GROUP BY p.id, p.name, p.client, p.currency, p.billable, p.hourly_rate_cents
ORDER BY seconds DESC;

-- Day buckets are cut in the caller's timezone, not UTC: a session that runs
-- 22:30–23:30 UTC belongs to the next local day in Istanbul, and a report that
-- says otherwise is simply wrong to the person reading it.
-- name: SummaryByDay :many
SELECT
    to_char(date_trunc('day', ws.started_at AT TIME ZONE sqlc.arg(tz)::text), 'YYYY-MM-DD') AS day,
    sum(extract(epoch FROM (
        least(ws.ended_at, sqlc.arg(to_ts)::timestamptz)
      - greatest(ws.started_at, sqlc.arg(from_ts)::timestamptz)
    )))::bigint AS seconds
FROM work_sessions ws
WHERE ws.user_id = sqlc.arg(user_id)
  AND ws.deleted_at IS NULL
  AND ws.ended_at IS NOT NULL
  AND ws.started_at < sqlc.arg(to_ts)::timestamptz
  AND ws.ended_at > sqlc.arg(from_ts)::timestamptz
GROUP BY 1
ORDER BY 1 ASC;
