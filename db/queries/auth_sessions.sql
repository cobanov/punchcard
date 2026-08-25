-- name: CreateAuthSession :one
INSERT INTO auth_sessions (id, user_id, token_hash, expires_at, absolute_expires_at, ip, user_agent)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetAuthSessionByHash :one
SELECT sqlc.embed(auth_sessions), sqlc.embed(users)
FROM auth_sessions
JOIN users ON users.id = auth_sessions.user_id
WHERE auth_sessions.token_hash = $1
  AND auth_sessions.revoked_at IS NULL
  AND auth_sessions.expires_at > now()
  AND auth_sessions.absolute_expires_at > now()
  AND users.deleted_at IS NULL;

-- name: TouchAuthSession :exec
UPDATE auth_sessions SET last_seen_at = now(), expires_at = $2 WHERE id = $1;

-- name: RevokeAuthSession :execrows
UPDATE auth_sessions SET revoked_at = now()
WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: ListAuthSessionsByUser :many
SELECT * FROM auth_sessions
WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
ORDER BY last_seen_at DESC;

-- name: RevokeAllUserAuthSessions :exec
UPDATE auth_sessions SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: RevokeOtherUserAuthSessions :exec
UPDATE auth_sessions SET revoked_at = now()
WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL;

-- name: DeleteStaleAuthSessions :execrows
DELETE FROM auth_sessions
WHERE absolute_expires_at < now()
   OR (revoked_at IS NOT NULL AND revoked_at < now() - interval '30 days');
