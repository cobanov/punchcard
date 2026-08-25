-- +goose Up

-- commits: the user's fetched commits, cached independently of any session.
--
-- Keeping them here rather than only inside session_commits is what makes
-- "unmatched commits" possible: a commit written while no timer was running has
-- nowhere to attach, and it is exactly that commit the product wants to show
-- back to the user so a forgotten session can be recovered. Deleting a session
-- therefore does not delete its commits — they simply become unmatched again.
CREATE TABLE commits (
    id             uuid PRIMARY KEY,
    user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    repo_full_name text NOT NULL,
    sha            text NOT NULL CHECK (char_length(sha) BETWEEN 7 AND 64),
    message        text NOT NULL DEFAULT '',
    committed_at   timestamptz NOT NULL,
    url            text NOT NULL DEFAULT '',
    additions      integer,
    deletions      integer,
    created_at     timestamptz NOT NULL DEFAULT now()
);
-- Re-scanning the same window must not duplicate rows; the scanner upserts on
-- this key.
CREATE UNIQUE INDEX idx_commits_unique ON commits (user_id, repo_full_name, sha);
-- Serves the unmatched-commit sweep and any per-window lookup.
CREATE INDEX idx_commits_user_time ON commits (user_id, committed_at DESC);

-- session_commits: which session a commit was attributed to.
CREATE TABLE session_commits (
    session_id uuid NOT NULL REFERENCES work_sessions(id) ON DELETE CASCADE,
    commit_id  uuid NOT NULL REFERENCES commits(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, commit_id)
);
-- A commit belongs to at most one session. Sessions cannot overlap (see
-- one_open_session_per_user), so this should never fire — which is exactly why
-- it is here: if it ever does, an overlap got in and the reports are wrong.
CREATE UNIQUE INDEX idx_session_commits_commit ON session_commits (commit_id);

-- github_connections: one OAuth connection per user.
--
-- The token is stored encrypted with AES-256-GCM under GITHUB_TOKEN_KEY. The
-- service refuses to start without that key rather than falling back to
-- plaintext, because a silent fallback is how a secret ends up in a backup.
CREATE TABLE github_connections (
    user_id          uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    github_login     text NOT NULL,
    access_token_enc bytea NOT NULL,
    scopes           text NOT NULL DEFAULT '',
    connected_at     timestamptz NOT NULL DEFAULT now(),
    last_scan_at     timestamptz,
    last_error       text,
    revoked_at       timestamptz
);

-- github_emails: extra commit-author addresses.
--
-- The scanner filters by GitHub login, which misses commits made on a machine
-- whose git config uses a different address. Rather than guess, the user tells
-- us which addresses are also theirs.
CREATE TABLE github_emails (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email   citext NOT NULL,
    PRIMARY KEY (user_id, email)
);

-- +goose Down
DROP TABLE github_emails;
DROP TABLE github_connections;
DROP TABLE session_commits;
DROP TABLE commits;
