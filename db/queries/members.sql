-- name: AddMember :exec
INSERT INTO list_members (list_id, user_id, role)
VALUES ($1, $2, $3)
ON CONFLICT (list_id, user_id) DO UPDATE SET role = EXCLUDED.role;

-- name: ListMembers :many
SELECT lm.list_id, lm.user_id, lm.role, lm.created_at, u.email
FROM list_members lm
JOIN users u ON u.id = lm.user_id
WHERE lm.list_id = @list_id
ORDER BY lm.created_at ASC
LIMIT @lim;

-- name: UpdateMemberRole :execrows
UPDATE list_members SET role = $3 WHERE list_id = $1 AND user_id = $2;

-- name: RemoveMember :execrows
DELETE FROM list_members WHERE list_id = $1 AND user_id = $2;

-- name: CountMembers :one
SELECT count(*) FROM list_members WHERE list_id = $1;
