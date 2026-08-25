-- +goose Up
-- Social login: provider identity stored as nullable
-- unique columns on users (bucketmark's pattern). password_hash stays NOT NULL;
-- OAuth-only accounts get an unusable random hash so the password plane can
-- never authenticate them.
ALTER TABLE users ADD COLUMN google_sub text;
ALTER TABLE users ADD COLUMN github_id text;
CREATE UNIQUE INDEX idx_users_google_sub ON users (google_sub) WHERE google_sub IS NOT NULL;
CREATE UNIQUE INDEX idx_users_github_id ON users (github_id) WHERE github_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_users_github_id;
DROP INDEX idx_users_google_sub;
ALTER TABLE users DROP COLUMN github_id;
ALTER TABLE users DROP COLUMN google_sub;
