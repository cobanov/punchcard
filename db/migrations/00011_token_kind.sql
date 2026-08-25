-- +goose Up
-- Tell a first-party device token apart from a third-party automation token.
--
-- 0.4.6 closed a real hole by making the account plane — delete, export, token
-- and session management, password, 2FA, profile — session-only: the export
-- reaches every list, every soft-deleted task and the metadata of the account's
-- other tokens, so an automation PAT must never read it.
--
-- That rule assumed "PAT means third party". It does not. The iOS and Android
-- shells cannot hold a session cookie (the WKWebView calls the API cross-origin
-- and iOS drops the cookie), so they authenticate with a device token minted by
-- MintDeviceToken at sign-in — and inherited the restriction. The whole Settings
-- screen was dead on the phone, account deletion included, which Apple requires
-- to be available in-app under Guideline 5.1.1(v).
--
-- So the distinction the code was missing is not the credential's shape but who
-- holds it. `kind` records that. Existing rows are 'pat': every token minted
-- before this migration was either a user-created PAT or a device token, and
-- defaulting the ambiguous ones to the *narrower* privilege is the safe way to
-- be wrong. A phone whose token predates this simply signs in again.
ALTER TABLE api_tokens
    ADD COLUMN kind text NOT NULL DEFAULT 'pat'
    CHECK (kind IN ('pat', 'device'));

-- +goose Down
ALTER TABLE api_tokens DROP COLUMN kind;
