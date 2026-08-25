-- name: CreateProject :one
INSERT INTO projects (id, owner_id, name, client, color, hourly_rate_cents, currency, billable)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetProjectForUser :one
SELECT * FROM projects
WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL;

-- name: ListProjects :many
SELECT * FROM projects
WHERE owner_id = @owner_id
  AND deleted_at IS NULL
  AND (@include_archived::boolean OR archived_at IS NULL)
ORDER BY lower(name) ASC;

-- name: UpdateProject :one
UPDATE projects SET
    name              = coalesce(sqlc.narg(name), name),
    client            = coalesce(sqlc.narg(client), client),
    color             = CASE WHEN sqlc.arg(clear_color)::boolean THEN NULL
                             ELSE coalesce(sqlc.narg(color), color) END,
    hourly_rate_cents = CASE WHEN sqlc.arg(clear_rate)::boolean THEN NULL
                             ELSE coalesce(sqlc.narg(hourly_rate_cents), hourly_rate_cents) END,
    currency          = coalesce(sqlc.narg(currency), currency),
    billable          = coalesce(sqlc.narg(billable), billable),
    updated_at        = now()
WHERE id = sqlc.arg(id) AND owner_id = sqlc.arg(owner_id) AND deleted_at IS NULL
RETURNING *;

-- name: ArchiveProject :one
UPDATE projects SET archived_at = now(), updated_at = now()
WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL AND archived_at IS NULL
RETURNING *;

-- name: UnarchiveProject :one
UPDATE projects SET archived_at = NULL, updated_at = now()
WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteProject :execrows
UPDATE projects SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND owner_id = $2 AND deleted_at IS NULL;

-- name: CountSessionsForProject :one
SELECT count(*) FROM work_sessions
WHERE project_id = $1 AND deleted_at IS NULL;

-- name: LinkProjectRepo :one
INSERT INTO project_repos (id, project_id, provider, full_name)
VALUES ($1, $2, 'github', $3)
ON CONFLICT (project_id, provider, full_name) DO NOTHING
RETURNING *;

-- name: GetProjectRepo :one
SELECT project_repos.* FROM project_repos
JOIN projects ON projects.id = project_repos.project_id
WHERE project_repos.id = $1 AND projects.owner_id = $2 AND projects.deleted_at IS NULL;

-- name: ListProjectRepos :many
SELECT * FROM project_repos WHERE project_id = $1 ORDER BY full_name ASC;

-- name: ListReposForUser :many
SELECT project_repos.* FROM project_repos
JOIN projects ON projects.id = project_repos.project_id
WHERE projects.owner_id = $1 AND projects.deleted_at IS NULL
ORDER BY project_repos.full_name ASC;

-- name: DeleteProjectRepo :execrows
DELETE FROM project_repos
USING projects
WHERE project_repos.id = $1
  AND project_repos.project_id = projects.id
  AND projects.owner_id = $2;

-- name: SetRepoBranches :exec
UPDATE project_repos SET branches = $2, branches_at = now() WHERE id = $1;

-- Which projects a repository belongs to, for suggesting a project when a
-- cluster of unmatched commits is turned into a session.
-- name: ProjectsForRepo :many
SELECT DISTINCT projects.id FROM projects
JOIN project_repos ON project_repos.project_id = projects.id
WHERE projects.owner_id = $1
  AND project_repos.full_name = $2
  AND projects.deleted_at IS NULL;
