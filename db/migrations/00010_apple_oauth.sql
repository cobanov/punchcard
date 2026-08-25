-- +goose Up
-- Sign in with Apple, stored the same way as the other two providers: a
-- nullable unique column on users rather than an identities table. Apple's
-- `sub` is stable per (app, user) and is the only durable identifier it gives
-- you — the email may be a private relay address and the name arrives exactly
-- once, on the first authorization.
ALTER TABLE users ADD COLUMN apple_sub text;
CREATE UNIQUE INDEX idx_users_apple_sub ON users (apple_sub) WHERE apple_sub IS NOT NULL;

-- +goose Down
DROP INDEX idx_users_apple_sub;
ALTER TABLE users DROP COLUMN apple_sub;
