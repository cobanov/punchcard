-- name: CreateTask :one
INSERT INTO tasks (id, list_id, title, body, status, due_at, priority, tags, parent_id, position, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetTaskForUser :one
-- Returns the task and the acting user's role on its list. No row => the user
-- is not a member (no task is reachable without membership).
SELECT sqlc.embed(tasks), lm.role
FROM tasks
JOIN list_members lm ON lm.list_id = tasks.list_id
WHERE tasks.id = $1 AND lm.user_id = $2;

-- name: UpdateTask :one
UPDATE tasks SET
    title = $2, body = $3, due_at = $4, priority = $5,
    tags = $6, position = $7, parent_id = $8, list_id = $9, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- Cross-list move support (subtasks travel with their parent).

-- name: ListSubtasks :many
SELECT * FROM tasks WHERE parent_id = $1 AND deleted_at IS NULL;

-- name: MoveSubtasksToList :many
UPDATE tasks SET list_id = $2, updated_at = now()
WHERE parent_id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: SetTaskStatus :one
UPDATE tasks SET
    status = $2,
    completed_at = CASE WHEN $2 = 'done' THEN now() ELSE NULL END,
    updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteTask :execrows
UPDATE tasks SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND deleted_at IS NULL;

-- name: RestoreTask :one
UPDATE tasks SET deleted_at = NULL, updated_at = now()
WHERE id = $1 AND deleted_at IS NOT NULL
RETURNING *;

-- name: MaxPositionInList :one
SELECT coalesce(max(position), '')::text FROM tasks WHERE list_id = $1 AND deleted_at IS NULL;

-- A new task lands at the top of its list, so creation needs the smallest
-- position rather than the largest. `min` over an empty list returns '' via the
-- coalesce, and PositionBetween('', '') is the midpoint — the same value the
-- first task in a list has always been given.
-- name: MinPositionInList :one
SELECT coalesce(min(position), '')::text FROM tasks WHERE list_id = $1 AND deleted_at IS NULL;

-- name: CountTasksInList :one
SELECT count(*) FROM tasks WHERE list_id = $1 AND deleted_at IS NULL;

-- name: CountSubtasks :one
SELECT count(*) FROM tasks WHERE parent_id = $1 AND deleted_at IS NULL;

-- name: PurgeDeletedTasks :execrows
DELETE FROM tasks WHERE deleted_at IS NOT NULL AND deleted_at < now() - interval '30 days';

-- name: ListTasksByPosition :many
SELECT sqlc.embed(tasks)
FROM tasks
JOIN list_members lm ON lm.list_id = tasks.list_id
WHERE lm.user_id = @user_id
  AND (sqlc.narg('list_id')::uuid IS NULL OR tasks.list_id = sqlc.narg('list_id'))
  AND (coalesce(cardinality(@scoped_list_ids::uuid[]), 0) = 0 OR tasks.list_id = ANY(@scoped_list_ids::uuid[]))
  AND (@status::text = '' OR tasks.status = @status)
  AND (@include_deleted::boolean OR tasks.deleted_at IS NULL)
  AND (sqlc.narg('due_before')::timestamptz IS NULL OR tasks.due_at <= sqlc.narg('due_before'))
  AND (sqlc.narg('due_after')::timestamptz IS NULL OR tasks.due_at >= sqlc.narg('due_after'))
  AND (@tag::text = '' OR tasks.tags ? @tag)
  AND (@q::text = '' OR (tasks.title ILIKE '%' || @q || '%' OR tasks.body ILIKE '%' || @q || '%'))
  AND (sqlc.narg('updated_since')::timestamptz IS NULL OR tasks.updated_at > sqlc.narg('updated_since'))
  AND (NOT @cursor_set::boolean OR (tasks.position, tasks.id) > (@cursor_position::text, @cursor_id::uuid))
ORDER BY tasks.position ASC, tasks.id ASC
LIMIT @lim;

-- name: ListTasksByCreated :many
SELECT sqlc.embed(tasks)
FROM tasks
JOIN list_members lm ON lm.list_id = tasks.list_id
WHERE lm.user_id = @user_id
  AND (sqlc.narg('list_id')::uuid IS NULL OR tasks.list_id = sqlc.narg('list_id'))
  AND (coalesce(cardinality(@scoped_list_ids::uuid[]), 0) = 0 OR tasks.list_id = ANY(@scoped_list_ids::uuid[]))
  AND (@status::text = '' OR tasks.status = @status)
  AND (@include_deleted::boolean OR tasks.deleted_at IS NULL)
  AND (sqlc.narg('due_before')::timestamptz IS NULL OR tasks.due_at <= sqlc.narg('due_before'))
  AND (sqlc.narg('due_after')::timestamptz IS NULL OR tasks.due_at >= sqlc.narg('due_after'))
  AND (@tag::text = '' OR tasks.tags ? @tag)
  AND (@q::text = '' OR (tasks.title ILIKE '%' || @q || '%' OR tasks.body ILIKE '%' || @q || '%'))
  AND (sqlc.narg('updated_since')::timestamptz IS NULL OR tasks.updated_at > sqlc.narg('updated_since'))
  AND (NOT @cursor_set::boolean OR (tasks.created_at, tasks.id) < (@cursor_time::timestamptz, @cursor_id::uuid))
ORDER BY tasks.created_at DESC, tasks.id DESC
LIMIT @lim;

-- name: ListTasksByUpdated :many
SELECT sqlc.embed(tasks)
FROM tasks
JOIN list_members lm ON lm.list_id = tasks.list_id
WHERE lm.user_id = @user_id
  AND (sqlc.narg('list_id')::uuid IS NULL OR tasks.list_id = sqlc.narg('list_id'))
  AND (coalesce(cardinality(@scoped_list_ids::uuid[]), 0) = 0 OR tasks.list_id = ANY(@scoped_list_ids::uuid[]))
  AND (@status::text = '' OR tasks.status = @status)
  AND (@include_deleted::boolean OR tasks.deleted_at IS NULL)
  AND (sqlc.narg('due_before')::timestamptz IS NULL OR tasks.due_at <= sqlc.narg('due_before'))
  AND (sqlc.narg('due_after')::timestamptz IS NULL OR tasks.due_at >= sqlc.narg('due_after'))
  AND (@tag::text = '' OR tasks.tags ? @tag)
  AND (@q::text = '' OR (tasks.title ILIKE '%' || @q || '%' OR tasks.body ILIKE '%' || @q || '%'))
  AND (sqlc.narg('updated_since')::timestamptz IS NULL OR tasks.updated_at > sqlc.narg('updated_since'))
  AND (NOT @cursor_set::boolean OR (tasks.updated_at, tasks.id) < (@cursor_time::timestamptz, @cursor_id::uuid))
ORDER BY tasks.updated_at DESC, tasks.id DESC
LIMIT @lim;
