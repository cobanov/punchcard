-- name: CreateAPIToken :one
INSERT INTO api_tokens (id, user_id, name, token_hash, token_prefix, scope, scoped_project_ids, expires_at, kind)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetAPITokenByHash :one
SELECT sqlc.embed(api_tokens), sqlc.embed(users)
FROM api_tokens
JOIN users ON users.id = api_tokens.user_id
WHERE api_tokens.token_hash = $1
  AND api_tokens.revoked_at IS NULL
  AND (api_tokens.expires_at IS NULL OR api_tokens.expires_at > now())
  AND users.deleted_at IS NULL;

-- name: ListTokensByUser :many
SELECT * FROM api_tokens
WHERE user_id = $1 AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: RevokeAPIToken :execrows
UPDATE api_tokens SET revoked_at = now()
WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = now() WHERE id = $1;

-- name: CountActiveTokens :one
SELECT count(*) FROM api_tokens WHERE user_id = $1 AND revoked_at IS NULL;
