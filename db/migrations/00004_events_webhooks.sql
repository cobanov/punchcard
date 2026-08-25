-- +goose Up

-- events: the transactional outbox. Every domain mutation writes a row here
-- in the same transaction; a poller fans out to webhooks and SSE.
CREATE TABLE events (
    id           uuid PRIMARY KEY,
    type         text NOT NULL,
    list_id      uuid REFERENCES lists(id) ON DELETE CASCADE,
    actor        text,
    payload      jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz
);
-- Outbox scan: unprocessed events in order.
CREATE INDEX idx_events_unprocessed ON events (created_at) WHERE processed_at IS NULL;
-- SSE resume (Last-Event-ID) window by list.
CREATE INDEX idx_events_list_created ON events (list_id, created_at);

-- webhooks: per-list delivery targets. Secret encrypted at rest.
CREATE TABLE webhooks (
    id               uuid PRIMARY KEY,
    list_id          uuid NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    url              text NOT NULL,
    secret_encrypted bytea NOT NULL,
    events           jsonb NOT NULL DEFAULT '[]',
    active           boolean NOT NULL DEFAULT true,
    created_by       uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    disabled_at      timestamptz,
    disabled_reason  text,
    consecutive_failures integer NOT NULL DEFAULT 0
);
CREATE INDEX idx_webhooks_list ON webhooks (list_id);

-- webhook_deliveries: per-attempt delivery log.
CREATE TABLE webhook_deliveries (
    id                    uuid PRIMARY KEY,
    webhook_id            uuid NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
    event_id              uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    attempt               integer NOT NULL DEFAULT 0,
    status                text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'success', 'failed', 'dead')),
    response_status       integer,
    response_body_snippet text,
    error                 text,
    delivered_at          timestamptz,
    next_retry_at         timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_deliveries_webhook ON webhook_deliveries (webhook_id, created_at DESC);
-- Retry scan: due, not-yet-terminal deliveries.
CREATE INDEX idx_deliveries_due ON webhook_deliveries (next_retry_at) WHERE status IN ('pending', 'failed');

-- +goose Down
DROP TABLE webhook_deliveries;
DROP TABLE webhooks;
DROP TABLE events;
