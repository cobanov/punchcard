-- +goose Up

-- Emails are citext so uniqueness is case-insensitive without every query
-- remembering to lower() them.
CREATE EXTENSION IF NOT EXISTS citext;

-- users: human accounts.
--
-- Social identity lives in nullable unique columns rather than a separate
-- identities table: there are three providers, they are not going to multiply,
-- and a join per sign-in buys nothing. password_hash stays NOT NULL — an
-- OAuth-only account gets an unusable random hash so the password plane can
-- never authenticate it.
--
-- timezone is the one column punchcard adds that helva did not have, and it is
-- load-bearing: every report draws its day boundaries in the user's zone, so a
-- session that crosses midnight in UTC still lands on the right local day.
CREATE TABLE users (
    id                uuid PRIMARY KEY,
    email             citext NOT NULL UNIQUE,
    password_hash     text NOT NULL,
    email_verified_at timestamptz,
    display_name      text,
    avatar_url        text,
    timezone          text NOT NULL DEFAULT 'UTC',
    google_sub        text,
    github_id         text,
    apple_sub         text,
    totp_secret_enc   bytea,
    totp_enabled      boolean NOT NULL DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);
CREATE UNIQUE INDEX idx_users_google_sub ON users (google_sub) WHERE google_sub IS NOT NULL;
CREATE UNIQUE INDEX idx_users_github_id ON users (github_id) WHERE github_id IS NOT NULL;
CREATE UNIQUE INDEX idx_users_apple_sub ON users (apple_sub) WHERE apple_sub IS NOT NULL;

-- auth_sessions: browser login sessions — opaque cookie tokens stored only as
-- SHA-256. Sliding expiry (expires_at) plus an absolute cap.
--
-- The name matters. In punchcard a "session" is the user's stretch of work, so
-- the login table cannot also be called `sessions`; see work_sessions in
-- 00002_domain.sql. Naming them apart here is cheaper than every reader of
-- every query having to work out which one is meant.
CREATE TABLE auth_sessions (
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
CREATE INDEX idx_auth_sessions_user ON auth_sessions (user_id) WHERE revoked_at IS NULL;

-- api_tokens: personal access tokens, stored as SHA-256.
--
-- `kind` separates a first-party device token from a third-party automation
-- token. The account plane (delete, export, token and session management,
-- password, 2FA) is closed to automation tokens because an export reaches every
-- record and the metadata of the account's other tokens; a device token belongs
-- to the user's own client and is trusted like a session.
CREATE TABLE api_tokens (
    id                 uuid PRIMARY KEY,
    user_id            uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name               text NOT NULL,
    token_hash         bytea NOT NULL UNIQUE,
    token_prefix       text NOT NULL,
    scope              text NOT NULL CHECK (scope IN ('read', 'read_write')),
    kind               text NOT NULL DEFAULT 'pat' CHECK (kind IN ('pat', 'device')),
    scoped_project_ids uuid[],
    created_at         timestamptz NOT NULL DEFAULT now(),
    expires_at         timestamptz,
    last_used_at       timestamptz,
    revoked_at         timestamptz
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

-- Two-factor recovery codes: hashes only, single use.
CREATE TABLE two_factor_recovery_codes (
    id         uuid PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  bytea NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_recovery_codes_user ON two_factor_recovery_codes (user_id);

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

-- idempotency_keys: replay protection for POSTs, scoped per user, purged by the
-- janitor.
CREATE TABLE idempotency_keys (
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key           text NOT NULL,
    request_hash  bytea NOT NULL,
    response_status integer NOT NULL,
    response_body bytea NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key)
);
CREATE INDEX idx_idempotency_created ON idempotency_keys (created_at);

-- +goose Down
DROP TABLE idempotency_keys;
DROP TABLE audit_log;
DROP TABLE two_factor_recovery_codes;
DROP TABLE email_tokens;
DROP TABLE api_tokens;
DROP TABLE auth_sessions;
DROP TABLE users;
DROP EXTENSION IF EXISTS citext;
