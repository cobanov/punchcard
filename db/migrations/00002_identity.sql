-- +goose Up

-- users: human accounts. email is citext for case-insensitive uniqueness.
CREATE TABLE users (
    id                uuid PRIMARY KEY,
    email             citext NOT NULL UNIQUE,
    password_hash     text NOT NULL,
    email_verified_at timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);

-- sessions: opaque cookie tokens, stored only as SHA-256 (bytea). Sliding
-- expiry (expires_at) plus an absolute cap (absolute_expires_at).
CREATE TABLE sessions (
    id                  uuid PRIMARY KEY,
    user_id             uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash          bytea NOT NULL UNIQUE,
    created_at          timestamptz NOT NULL DEFAULT now(),
    expires_at          timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    last_seen_at        timestamptz NOT NULL DEFAULT now(),
    ip                  text,
    user_agent          text,
    revoked_at          timestamptz
);
-- Serves session lookup/listing for a user (active sessions only).
CREATE INDEX idx_sessions_user ON sessions (user_id) WHERE revoked_at IS NULL;

-- api_tokens: personal access tokens (tdo_...), stored as SHA-256.
CREATE TABLE api_tokens (
    id              uuid PRIMARY KEY,
    user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name            text NOT NULL,
    token_hash      bytea NOT NULL UNIQUE,
    token_prefix    text NOT NULL,
    scope           text NOT NULL CHECK (scope IN ('read', 'read_write')),
    scoped_list_ids uuid[],
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz,
    last_used_at    timestamptz,
    revoked_at      timestamptz
);
CREATE INDEX idx_api_tokens_user ON api_tokens (user_id) WHERE revoked_at IS NULL;

-- email_tokens: single-use, hashed, expiring tokens for email verification and
-- password reset.
CREATE TABLE email_tokens (
    id         uuid PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('verify_email', 'password_reset')),
    token_hash bytea NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);
CREATE INDEX idx_email_tokens_user ON email_tokens (user_id, kind);

-- audit_log: security-relevant actions. metadata carries NO secrets.
CREATE TABLE audit_log (
    id         uuid PRIMARY KEY,
    user_id    uuid REFERENCES users(id) ON DELETE SET NULL,
    action     text NOT NULL,
    ip         text,
    metadata   jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_log_user ON audit_log (user_id, created_at DESC);

-- +goose Down
DROP TABLE audit_log;
DROP TABLE email_tokens;
DROP TABLE api_tokens;
DROP TABLE sessions;
DROP TABLE users;
