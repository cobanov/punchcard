-- name: CreateInvite :one
INSERT INTO invites (id, list_id, email, role, token_hash, invited_by, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (list_id, email) WHERE accepted_at IS NULL
DO UPDATE SET role = EXCLUDED.role, token_hash = EXCLUDED.token_hash,
    invited_by = EXCLUDED.invited_by, created_at = now(), expires_at = EXCLUDED.expires_at
RETURNING *;

-- name: GetInviteByHash :one
SELECT * FROM invites
WHERE token_hash = $1 AND accepted_at IS NULL AND expires_at > now();

-- name: GetInvite :one
SELECT * FROM invites WHERE id = $1;

-- name: ListInvitesForList :many
SELECT * FROM invites
WHERE list_id = $1 AND accepted_at IS NULL
ORDER BY created_at DESC;

-- name: ListPendingInvitesForEmail :many
SELECT sqlc.embed(invites), l.name AS list_name
FROM invites
JOIN lists l ON l.id = invites.list_id
WHERE invites.email = $1 AND invites.accepted_at IS NULL
  AND invites.expires_at > now() AND l.deleted_at IS NULL
ORDER BY invites.created_at DESC;

-- name: MarkInviteAccepted :execrows
UPDATE invites SET accepted_at = now() WHERE id = $1 AND accepted_at IS NULL;

-- name: DeleteInvite :execrows
DELETE FROM invites WHERE id = $1 AND list_id = $2 AND accepted_at IS NULL;

-- name: CountPendingInvitesByInviter :one
SELECT count(*) FROM invites WHERE invited_by = $1 AND accepted_at IS NULL;
