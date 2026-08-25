-- name: InsertAuditLog :exec
INSERT INTO audit_log (id, user_id, action, ip, metadata)
VALUES ($1, $2, $3, $4, $5);

-- name: ListAuditForUser :many
SELECT * FROM audit_log
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2;
