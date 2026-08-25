-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE user_id = $1 AND key = $2;

-- name: CreateIdempotencyKey :execrows
INSERT INTO idempotency_keys (user_id, key, request_hash, response_status, response_body)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, key) DO NOTHING;

-- name: DeleteExpiredIdempotencyKeys :execrows
DELETE FROM idempotency_keys WHERE created_at < now() - interval '24 hours';
