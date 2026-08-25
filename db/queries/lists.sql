-- name: CreateList :one
INSERT INTO lists (id, name, owner_id, is_personal)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetListForUser :one
SELECT sqlc.embed(lists), lm.role
FROM lists
JOIN list_members lm ON lm.list_id = lists.id
WHERE lists.id = $1 AND lm.user_id = $2 AND lists.deleted_at IS NULL;

-- name: ListListsForUser :many
SELECT sqlc.embed(lists), lm.role
FROM lists
JOIN list_members lm ON lm.list_id = lists.id
WHERE lm.user_id = @user_id AND lists.deleted_at IS NULL
ORDER BY lists.is_personal DESC, lists.created_at ASC
LIMIT @lim;

-- name: UpdateList :one
-- Partial by construction: a NULL argument leaves the column alone, so renaming
-- a list cannot clear its colour and setting a colour cannot rename it. Two
-- statements would have needed two round trips and two outbox events for what
-- is one edit.
--
-- Clearing the colour therefore cannot be expressed as NULL — that is "leave
-- it" — so it is the empty string, which the service translates. The column
-- itself only ever holds NULL or a name from the CHECK.
-- The `::text` casts are load-bearing. Without them Postgres meets `color`
-- for the first time in `IS NULL`, which carries no type information, and
-- refuses the statement outright: "could not determine data type of parameter
-- $3" (42P08). sqlc validates against the schema and Go compiles either way, so
-- this only ever surfaces against a real database.
UPDATE lists SET
    name = COALESCE(sqlc.narg('name')::text, name),
    color = CASE
        WHEN sqlc.narg('color')::text IS NULL THEN color
        WHEN sqlc.narg('color')::text = '' THEN NULL
        ELSE sqlc.narg('color')::text
    END,
    updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteList :execrows
UPDATE lists SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND deleted_at IS NULL;

-- name: CountListsForUser :one
SELECT count(*) FROM lists l
JOIN list_members lm ON lm.list_id = l.id
WHERE lm.user_id = $1 AND l.deleted_at IS NULL;

-- name: GetListMembership :one
SELECT role FROM list_members WHERE list_id = $1 AND user_id = $2;

-- name: ListAccessibleListIDs :many
SELECT l.id FROM lists l
JOIN list_members m ON m.list_id = l.id
WHERE m.user_id = $1 AND l.deleted_at IS NULL;

-- name: ListSharedListsOwnedBy :many
SELECT l.id, l.name FROM lists l
JOIN list_members m ON m.list_id = l.id
WHERE l.owner_id = $1 AND l.deleted_at IS NULL
GROUP BY l.id, l.name
HAVING count(m.user_id) > 1;

-- Task events carry a list id but not a list name, and the log snapshots names
-- so history does not rewrite itself when a list is renamed. One indexed
-- primary-key lookup inside a transaction that is already multi-statement.
-- name: GetListName :one
SELECT name FROM lists WHERE id = $1;
