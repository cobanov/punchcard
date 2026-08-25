-- name: UpsertGitHubConnection :one
INSERT INTO github_connections (user_id, github_login, access_token_enc, scopes)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id) DO UPDATE SET
    github_login     = EXCLUDED.github_login,
    access_token_enc = EXCLUDED.access_token_enc,
    scopes           = EXCLUDED.scopes,
    connected_at     = now(),
    last_error       = NULL,
    revoked_at       = NULL
RETURNING *;

-- name: GetGitHubConnection :one
SELECT * FROM github_connections WHERE user_id = $1 AND revoked_at IS NULL;

-- name: DeleteGitHubConnection :execrows
DELETE FROM github_connections WHERE user_id = $1;

-- name: SetGitHubError :exec
UPDATE github_connections SET last_error = $2 WHERE user_id = $1;

-- name: TouchGitHubScan :exec
UPDATE github_connections SET last_scan_at = now(), last_error = NULL WHERE user_id = $1;

-- name: AddGitHubEmail :exec
INSERT INTO github_emails (user_id, email) VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: ListGitHubEmails :many
SELECT email FROM github_emails WHERE user_id = $1 ORDER BY email;

-- name: DeleteGitHubEmail :execrows
DELETE FROM github_emails WHERE user_id = $1 AND email = $2;

-- Commits ------------------------------------------------------------------

-- Upsert so a re-scan of the same window is free of duplicates. The message and
-- URL are refreshed because an amended commit keeps its position in history but
-- not its text.
-- name: UpsertCommit :one
INSERT INTO commits (id, user_id, repo_full_name, sha, message, committed_at, url)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (user_id, repo_full_name, sha) DO UPDATE SET
    message = EXCLUDED.message,
    url     = EXCLUDED.url
RETURNING *;

-- name: AttachCommitToSession :execrows
INSERT INTO session_commits (session_id, commit_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: DetachCommitsFromSession :execrows
DELETE FROM session_commits WHERE session_id = $1;

-- name: ListCommitsForSession :many
SELECT c.* FROM commits c
JOIN session_commits sc ON sc.commit_id = c.id
WHERE sc.session_id = $1
ORDER BY c.committed_at ASC;

-- name: CountCommitsForSessions :many
SELECT sc.session_id, count(*)::bigint AS n
FROM session_commits sc
WHERE sc.session_id = ANY(sqlc.arg(session_ids)::uuid[])
GROUP BY sc.session_id;

-- Commits in the window that belong to no session. This is the feed behind
-- "unmatched commits": work that happened while no timer was running.
-- name: ListUnmatchedCommits :many
SELECT c.* FROM commits c
LEFT JOIN session_commits sc ON sc.commit_id = c.id
WHERE c.user_id = sqlc.arg(user_id)
  AND sc.commit_id IS NULL
  AND c.committed_at >= sqlc.arg(from_ts)::timestamptz
  AND c.committed_at <= sqlc.arg(to_ts)::timestamptz
ORDER BY c.committed_at ASC;

-- name: ListCommitsInWindow :many
SELECT * FROM commits
WHERE user_id = sqlc.arg(user_id)
  AND committed_at >= sqlc.arg(from_ts)::timestamptz
  AND committed_at <= sqlc.arg(to_ts)::timestamptz
ORDER BY committed_at ASC;
