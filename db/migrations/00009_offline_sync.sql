-- +goose Up

-- events.seq: a monotonic cursor for the offline-sync delta feed and SSE resume
-- (docs/offline-sync.md §5). The previous created_at cursor could skip rows
-- that share a timestamp tick; seq is strictly ordered. Values are allocated at
-- INSERT time, so readers additionally apply a small freshness grace window to
-- avoid racing transactions that commit out of seq order.
ALTER TABLE events ADD COLUMN seq bigint;
CREATE SEQUENCE events_seq OWNED BY events.seq;
ALTER TABLE events ALTER COLUMN seq SET DEFAULT nextval('events_seq');

-- Backfill existing rows deterministically in (created_at, id) order so
-- historical order matches what the old timestamp cursor would have served.
WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS rn FROM events
)
UPDATE events SET seq = ordered.rn FROM ordered WHERE events.id = ordered.id;
SELECT setval('events_seq', COALESCE((SELECT max(seq) FROM events), 0) + 1, false);

ALTER TABLE events ALTER COLUMN seq SET NOT NULL;
CREATE UNIQUE INDEX idx_events_seq ON events (seq);

-- +goose Down
DROP INDEX idx_events_seq;
ALTER TABLE events DROP COLUMN seq; -- drops the owned sequence with it
