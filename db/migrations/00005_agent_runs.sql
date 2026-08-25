-- +goose Up

-- agent_runs: working intervals reported by local agent hooks, cached
-- independently of any session — same reasoning as the commits table: a run
-- that happened while no timer was running is exactly the run the product
-- wants to surface back, and deleting a session must not delete its evidence.
--
-- The difference from commits, and it matters: a commit is proof punchcard
-- fetched from GitHub itself, while a run is a client's claim that punchcard
-- has no way to check. Nothing here is verified. Readers should present the
-- two differently, and no report should treat a run as billable time.
CREATE TABLE agent_runs (
    id             uuid PRIMARY KEY,
    user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tool           text NOT NULL CHECK (char_length(tool) BETWEEN 1 AND 64),
    -- Client-generated idempotency key; the queue is flushed at-least-once,
    -- so re-sending a batch must not duplicate rows.
    external_id    text NOT NULL CHECK (char_length(external_id) BETWEEN 1 AND 200),
    started_at     timestamptz NOT NULL,
    ended_at       timestamptz NOT NULL,
    model          text NOT NULL DEFAULT '',
    cwd            text NOT NULL DEFAULT '',
    repo_full_name text NOT NULL DEFAULT '',
    tool_calls     integer,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CHECK (ended_at >= started_at)
);
CREATE UNIQUE INDEX idx_agent_runs_unique ON agent_runs (user_id, tool, external_id);
CREATE INDEX idx_agent_runs_user_time ON agent_runs (user_id, started_at DESC);

-- session_agent_runs: which session a run was attributed to. UNIQUE on the
-- run for the same reason session_commits has it: sessions cannot overlap,
-- so a run in two sessions means the overlap invariant broke somewhere.
CREATE TABLE session_agent_runs (
    session_id   uuid NOT NULL REFERENCES work_sessions(id) ON DELETE CASCADE,
    agent_run_id uuid NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, agent_run_id)
);
CREATE UNIQUE INDEX idx_session_agent_runs_run ON session_agent_runs (agent_run_id);

-- +goose Down
DROP TABLE session_agent_runs;
DROP TABLE agent_runs;
