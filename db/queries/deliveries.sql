-- name: CreateDelivery :one
INSERT INTO webhook_deliveries (id, webhook_id, event_id, status, next_retry_at)
VALUES ($1, $2, $3, 'pending', now())
RETURNING *;

-- name: ListDeliveriesForWebhook :many
SELECT * FROM webhook_deliveries WHERE webhook_id = $1 ORDER BY created_at DESC LIMIT $2;

-- name: GetDelivery :one
SELECT * FROM webhook_deliveries WHERE id = $1;

-- name: ClaimDueDeliveries :many
SELECT * FROM webhook_deliveries
WHERE status IN ('pending', 'failed') AND next_retry_at IS NOT NULL AND next_retry_at <= now()
ORDER BY next_retry_at ASC
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: UpdateDeliveryResult :exec
UPDATE webhook_deliveries SET
    status = $2, attempt = $3, response_status = $4,
    response_body_snippet = $5, error = $6, delivered_at = $7, next_retry_at = $8
WHERE id = $1;
