-- name: CreateEmailToken :one
INSERT INTO email_tokens (id, user_id, kind, token_hash, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetValidEmailToken :one
SELECT * FROM email_tokens
WHERE token_hash = $1 AND kind = $2 AND used_at IS NULL AND expires_at > now();

-- name: MarkEmailTokenUsed :execrows
UPDATE email_tokens SET used_at = now() WHERE id = $1 AND used_at IS NULL;

-- name: DeleteUserEmailTokens :exec
DELETE FROM email_tokens WHERE user_id = $1 AND kind = $2;
