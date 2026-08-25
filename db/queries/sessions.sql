-- name: CreateSession :one
INSERT INTO sessions (id, user_id, token_hash, expires_at, absolute_expires_at, ip, user_agent)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetSessionByHash :one
SELECT sqlc.embed(sessions), sqlc.embed(users)
FROM sessions
JOIN users ON users.id = sessions.user_id
WHERE sessions.token_hash = $1
  AND sessions.revoked_at IS NULL
  AND sessions.expires_at > now()
  AND sessions.absolute_expires_at > now()
  AND users.deleted_at IS NULL;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = now(), expires_at = $2 WHERE id = $1;

-- name: RevokeSession :execrows
UPDATE sessions SET revoked_at = now()
WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: ListSessionsByUser :many
SELECT * FROM sessions
WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
ORDER BY last_seen_at DESC;

-- name: RevokeAllUserSessions :exec
UPDATE sessions SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: RevokeOtherUserSessions :exec
UPDATE sessions SET revoked_at = now()
WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL;

-- name: DeleteStaleSessions :execrows
DELETE FROM sessions
WHERE absolute_expires_at < now()
   OR (revoked_at IS NOT NULL AND revoked_at < now() - interval '30 days');
