-- name: CreateWebhook :one
INSERT INTO webhooks (id, user_id, url, secret_encrypted, events)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListWebhooksForUser :many
SELECT * FROM webhooks WHERE user_id = @user_id ORDER BY created_at DESC LIMIT @lim;

-- name: GetWebhook :one
SELECT * FROM webhooks WHERE id = $1;

-- name: ListActiveWebhooksForUser :many
SELECT * FROM webhooks WHERE user_id = @user_id AND active = true LIMIT @lim;

-- name: CountWebhooksForUser :one
SELECT count(*) FROM webhooks WHERE user_id = $1;

-- name: UpdateWebhook :one
UPDATE webhooks SET
    url = $2,
    events = $3,
    active = $4,
    disabled_at = CASE WHEN $4 THEN NULL ELSE disabled_at END,
    disabled_reason = CASE WHEN $4 THEN NULL ELSE disabled_reason END,
    consecutive_failures = CASE WHEN $4 THEN 0 ELSE consecutive_failures END
WHERE id = $1
RETURNING *;

-- name: RotateWebhookSecret :one
UPDATE webhooks SET secret_encrypted = $2 WHERE id = $1 RETURNING *;

-- name: DeleteWebhook :execrows
DELETE FROM webhooks WHERE id = $1 AND user_id = $2;

-- name: RecordWebhookSuccess :exec
UPDATE webhooks SET consecutive_failures = 0 WHERE id = $1;

-- name: RecordWebhookFailure :one
UPDATE webhooks SET consecutive_failures = consecutive_failures + 1 WHERE id = $1
RETURNING consecutive_failures;

-- name: DisableWebhook :exec
UPDATE webhooks SET active = false, disabled_at = now(), disabled_reason = $2 WHERE id = $1;
