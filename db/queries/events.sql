-- name: InsertEvent :one
INSERT INTO events (id, type, list_id, actor, payload)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetEvent :one
SELECT * FROM events WHERE id = $1;

-- name: ClaimUnprocessedEvents :many
SELECT * FROM events
WHERE processed_at IS NULL
ORDER BY created_at ASC
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: MarkEventProcessed :exec
UPDATE events SET processed_at = now() WHERE id = $1;

-- Delta feed + SSE cursor reads (docs/offline-sync.md §5). The grace window is
-- the seq gap-safety guard: bigserial values are allocated at INSERT, so a
-- still-uncommitted transaction can hold a smaller seq than rows already
-- visible; excluding very fresh rows keeps a forward-only cursor from skipping
-- them permanently.

-- name: ListEventsForListsAfterSeq :many
SELECT * FROM events
WHERE list_id = ANY(sqlc.arg(list_ids)::uuid[])
  AND seq > sqlc.arg(after_seq)
  AND created_at < now() - make_interval(secs => sqlc.arg(grace_secs)::double precision)
ORDER BY seq ASC
LIMIT sqlc.arg(lim);

-- name: MaxEventSeq :one
SELECT COALESCE(max(seq), 0)::bigint FROM events
WHERE created_at < now() - make_interval(secs => sqlc.arg(grace_secs)::double precision);

-- name: MinEventSeq :one
SELECT COALESCE(min(seq), 0)::bigint FROM events;

-- name: PurgeOldEvents :execrows
DELETE FROM events WHERE created_at < now() - interval '30 days';
