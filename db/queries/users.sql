-- name: CreateUser :one
INSERT INTO users (id, email, password_hash)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1 AND deleted_at IS NULL;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1 AND deleted_at IS NULL;

-- name: MarkEmailVerified :exec
UPDATE users SET email_verified_at = now(), updated_at = now()
WHERE id = $1 AND email_verified_at IS NULL;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1;

-- name: UpdateUserProfile :one
UPDATE users SET display_name = $2, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: SetTOTPSecret :exec
UPDATE users SET totp_secret_enc = $2, updated_at = now() WHERE id = $1;

-- name: SetTOTPEnabled :exec
UPDATE users SET totp_enabled = $2, updated_at = now() WHERE id = $1;

-- name: ClearTOTP :exec
UPDATE users SET totp_secret_enc = NULL, totp_enabled = false, updated_at = now() WHERE id = $1;

-- name: AddRecoveryCode :exec
INSERT INTO two_factor_recovery_codes (id, user_id, code_hash) VALUES ($1, $2, $3);

-- name: DeleteRecoveryCodes :exec
DELETE FROM two_factor_recovery_codes WHERE user_id = $1;

-- name: UseRecoveryCode :execrows
UPDATE two_factor_recovery_codes SET used_at = now()
WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL;

-- name: CountUnusedRecoveryCodes :one
SELECT count(*) FROM two_factor_recovery_codes WHERE user_id = $1 AND used_at IS NULL;

-- name: UpdateUserAvatar :one
UPDATE users SET avatar_url = $2, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: UpdateUserEmail :one
UPDATE users SET email = $2, email_verified_at = NULL, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: GetUserByGoogleSub :one
SELECT * FROM users WHERE google_sub = $1 AND deleted_at IS NULL;

-- name: GetUserByGithubID :one
SELECT * FROM users WHERE github_id = $1 AND deleted_at IS NULL;

-- name: GetUserByAppleSub :one
SELECT * FROM users WHERE apple_sub = $1 AND deleted_at IS NULL;

-- name: CreateOAuthUser :one
-- Creates an account linked to a verified social identity. password_hash holds
-- an unusable random hash (the caller has no way to know the plaintext) so the
-- password plane can never authenticate an OAuth-only account. Email is treated
-- as verified because the provider asserted verification.
INSERT INTO users (id, email, password_hash, email_verified_at, google_sub, github_id, apple_sub)
VALUES ($1, $2, $3, now(), $4, $5, $6)
RETURNING *;

-- name: LinkGoogleSub :exec
UPDATE users
SET google_sub = $2, email_verified_at = COALESCE(email_verified_at, now()), updated_at = now()
WHERE id = $1;

-- name: LinkGithubID :exec
UPDATE users
SET github_id = $2, email_verified_at = COALESCE(email_verified_at, now()), updated_at = now()
WHERE id = $1;

-- name: LinkAppleSub :exec
UPDATE users
SET apple_sub = $2, email_verified_at = COALESCE(email_verified_at, now()), updated_at = now()
WHERE id = $1;

-- name: SoftDeleteUser :exec
UPDATE users SET deleted_at = now(), updated_at = now() WHERE id = $1;

-- name: HardDeleteUser :exec
DELETE FROM users WHERE id = $1;

-- name: UpdateUserTimezone :one
UPDATE users SET timezone = $2, updated_at = now() WHERE id = $1 RETURNING *;
