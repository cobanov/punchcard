-- name: InsertActivity :exec
INSERT INTO activity (id, user_id, list_id, origin, action, subject, list_name, detail, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- Two clauses, deliberately not one. The first is AUTHORIZATION: what this
-- principal may read at all. The second is DISPLAY: what the user asked to
-- see. Collapsing them is how a filter becomes a permission by accident, which
-- is the shape of the defects closed in 0.4.6.
--
-- own_user_id is uuid_nil for a list-scoped PAT, which drops the "my own
-- actions" branch entirely — without that, such a token reads the user's
-- activity on lists outside its scope straight through the OR.
-- name: ListActivity :many
SELECT sqlc.embed(a), u.display_name, u.email
FROM activity a
JOIN users u ON u.id = a.user_id
WHERE (a.user_id = @own_user_id::uuid OR a.list_id = ANY(@list_ids::uuid[]))
  AND (NOT @mine::boolean OR a.user_id = @me::uuid)
  AND (cardinality(@origins::text[]) = 0 OR a.origin = ANY(@origins::text[]))
  AND (a.occurred_at, a.id) < (@before_time::timestamptz, @before_id::uuid)
ORDER BY a.occurred_at DESC, a.id DESC
LIMIT @lim;

-- name: PurgeOldActivity :execrows
DELETE FROM activity WHERE occurred_at < now() - interval '400 days';
