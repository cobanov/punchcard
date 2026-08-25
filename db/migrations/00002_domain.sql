-- +goose Up

-- projects: what time is booked against. One owner, no sharing in v1 — the
-- membership machinery the chassis carries is deliberately not wired up here.
--
-- hourly_rate_cents is integer minor units. Money never touches a float in this
-- schema or in the code that reads it: a rate of 333.33 and ninety minutes has
-- exactly one right answer and binary floating point is not how you get it.
--
-- color is a NAME, not a hex value. The colour is resolved to a palette token
-- at paint time, so the same project reads as "red" in every theme a client
-- chooses and the palette can be retuned without a data migration. The CHECK
-- lives here rather than only in the service because this column is written
-- from the HTTP API and will be written by CLI and agent clients too.
CREATE TABLE projects (
    id                uuid PRIMARY KEY,
    owner_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name              text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    client            text NOT NULL DEFAULT '' CHECK (char_length(client) <= 200),
    color             text CHECK (color IS NULL OR color IN
                          ('red', 'amber', 'green', 'teal', 'blue', 'violet', 'pink', 'slate')),
    hourly_rate_cents bigint CHECK (hourly_rate_cents IS NULL OR hourly_rate_cents >= 0),
    currency          char(3) NOT NULL DEFAULT 'TRY',
    billable          boolean NOT NULL DEFAULT true,
    archived_at       timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);
-- One project per name per owner, case-insensitively: "Capsarsiv" and
-- "capsarsiv" are the same project to a human, so they are the same here.
CREATE UNIQUE INDEX idx_projects_owner_name ON projects (owner_id, lower(name))
    WHERE deleted_at IS NULL;
-- Hot read: the picker's list of live, unarchived projects.
CREATE INDEX idx_projects_owner_active ON projects (owner_id)
    WHERE deleted_at IS NULL AND archived_at IS NULL;

-- project_repos: the GitHub repositories a project's work shows up in.
--
-- Many-to-many on purpose. A repository can belong to several projects (client
-- work and an internal tool can share a monorepo) because which session a
-- commit lands in is decided by TIME, not by repository. `branches` caches the
-- branch list so the scanner does not re-fetch it on every run; `branches_at`
-- is how it knows the cache is stale.
CREATE TABLE project_repos (
    id          uuid PRIMARY KEY,
    project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    provider    text NOT NULL DEFAULT 'github' CHECK (provider IN ('github')),
    full_name   text NOT NULL CHECK (full_name ~ '^[^/[:space:]]+/[^/[:space:]]+$'),
    branches    jsonb NOT NULL DEFAULT '[]',
    branches_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_project_repos_unique ON project_repos (project_id, provider, full_name);
CREATE INDEX idx_project_repos_full_name ON project_repos (full_name);

-- work_sessions: a stretch of work. ended_at IS NULL means the timer is running.
--
-- Duration is NOT stored. It is ended_at - started_at, computed on read, so
-- there is exactly one source of truth and no way for a stored duration to
-- disagree with the times a user just corrected.
--
-- sync_* is the GitHub scan state machine, kept on the row rather than in a
-- separate job table: the work to be done IS "scan this session's window", so a
-- second table would only restate what this row already knows.
CREATE TABLE work_sessions (
    id            uuid PRIMARY KEY,
    project_id    uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    note          text NOT NULL DEFAULT '' CHECK (char_length(note) <= 500),
    started_at    timestamptz NOT NULL,
    ended_at      timestamptz,
    source        text NOT NULL DEFAULT 'web'
                  CHECK (source IN ('web', 'cli', 'extension', 'mobile', 'auto')),
    sync_state    text NOT NULL DEFAULT 'pending'
                  CHECK (sync_state IN ('pending', 'ok', 'error', 'skipped')),
    sync_attempts smallint NOT NULL DEFAULT 0,
    sync_next_at  timestamptz,
    sync_error    text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz,
    CONSTRAINT work_sessions_range CHECK (ended_at IS NULL OR ended_at > started_at)
);
-- One open session per user, enforced by the database.
--
-- This is not a convenience. Two open sessions would make overlapping time
-- ranges possible, and the whole commit-attribution design rests on ranges NOT
-- overlapping: a commit falls in at most one session because at most one
-- session covers any instant. Leaving that to the service layer would mean a
-- race between two clients could silently corrupt every report that follows.
CREATE UNIQUE INDEX one_open_session_per_user ON work_sessions (user_id)
    WHERE ended_at IS NULL AND deleted_at IS NULL;
-- Hot read: "today", and any date-range listing.
CREATE INDEX idx_work_sessions_user_started ON work_sessions (user_id, started_at DESC)
    WHERE deleted_at IS NULL;
-- Report grouping by project.
CREATE INDEX idx_work_sessions_project ON work_sessions (project_id, started_at);
-- The janitor's scan queue.
CREATE INDEX idx_work_sessions_sync ON work_sessions (sync_next_at)
    WHERE sync_state = 'pending';

-- +goose Down
DROP TABLE work_sessions;
DROP TABLE project_repos;
DROP TABLE projects;
