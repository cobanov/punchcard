-- +goose Up
-- TOTP two-factor auth (ATD-18). The secret is stored AES-GCM encrypted (keyed
-- off APP_SECRET); recovery codes are stored only as hashes, single-use.
ALTER TABLE users ADD COLUMN totp_secret_enc bytea;
ALTER TABLE users ADD COLUMN totp_enabled boolean NOT NULL DEFAULT false;

CREATE TABLE two_factor_recovery_codes (
    id         uuid PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  bytea NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_recovery_codes_user ON two_factor_recovery_codes(user_id);

-- +goose Down
DROP TABLE two_factor_recovery_codes;
ALTER TABLE users DROP COLUMN totp_enabled;
ALTER TABLE users DROP COLUMN totp_secret_enc;
