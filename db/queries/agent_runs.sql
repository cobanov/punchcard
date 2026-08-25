-- Agent runs -----------------------------------------------------------------

-- Insert, or say nothing if this run is already known.
--
-- DO NOTHING rather than DO UPDATE: the queue is flushed at-least-once and a
-- resend carries the same facts, so there is nothing to refresh. Returning no
-- row on conflict is how the caller counts duplicates without a second query.
-- name: InsertAgentRun :one
INSERT INTO agent_runs (
    id, user_id, tool, external_id, started_at, ended_at,
    model, cwd, repo_full_name, tool_calls
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (user_id, tool, external_id) DO NOTHING
RETURNING *;

-- Attach every unattached run whose START falls inside a finished session.
--
-- The range is half-open — [started_at, ended_at) — unlike the commit lookup,
-- which uses a closed range. A closed range lets a run sitting exactly on a
-- boundary match both the session that ends there and the one that begins
-- there, and StartSession deliberately butts sessions up against each other a
-- microsecond apart. Half-open makes that impossible rather than unlikely.
-- name: AttachCoveredAgentRuns :execrows
INSERT INTO session_agent_runs (session_id, agent_run_id)
SELECT ws.id, ar.id
FROM agent_runs ar
JOIN work_sessions ws
  ON ws.user_id    = ar.user_id
 AND ws.deleted_at IS NULL
 AND ws.ended_at   IS NOT NULL
 AND ar.started_at >= ws.started_at
 AND ar.started_at <  ws.ended_at
LEFT JOIN session_agent_runs sar ON sar.agent_run_id = ar.id
WHERE ar.user_id = sqlc.arg(user_id)
  AND sar.agent_run_id IS NULL
ON CONFLICT DO NOTHING;

-- Release runs whose session no longer covers them.
--
-- Editing a session's times is the whole reason this exists: shrink a session
-- and the runs it used to hold have to become unmatched again, or they stay
-- filed under a stretch of work that no longer claims them.
-- name: DetachUncoveredAgentRuns :execrows
DELETE FROM session_agent_runs sar
USING agent_runs ar, work_sessions ws
WHERE sar.agent_run_id = ar.id
  AND sar.session_id   = ws.id
  AND ar.user_id       = sqlc.arg(user_id)
  AND NOT (
        ws.deleted_at IS NULL
    AND ws.ended_at   IS NOT NULL
    AND ar.started_at >= ws.started_at
    AND ar.started_at <  ws.ended_at
  );

-- name: DetachAgentRunsFromSession :execrows
DELETE FROM session_agent_runs WHERE session_id = $1;

-- name: ListAgentRunsForSession :many
SELECT ar.* FROM agent_runs ar
JOIN session_agent_runs sar ON sar.agent_run_id = ar.id
WHERE sar.session_id = $1
ORDER BY ar.started_at ASC;

-- Runs in the window that belong to no session: the feed behind "an agent was
-- working here and no timer was running".
-- name: ListUnmatchedAgentRuns :many
SELECT ar.* FROM agent_runs ar
LEFT JOIN session_agent_runs sar ON sar.agent_run_id = ar.id
WHERE ar.user_id = sqlc.arg(user_id)
  AND sar.agent_run_id IS NULL
  AND ar.started_at >= sqlc.arg(from_ts)::timestamptz
  AND ar.started_at <= sqlc.arg(to_ts)::timestamptz
ORDER BY ar.started_at ASC;
