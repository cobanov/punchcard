-- +goose Up

-- events: the transactional outbox. Every domain mutation writes a row here in
-- the same transaction; a poller fans out to webhooks and SSE.
--
-- Scoped to the account rather than to a shared container: a project has one
-- owner, so user_id is what a subscriber filters on. project_id rides along so
-- a client can tell which project moved without unpacking the payload, and is
-- nullable because account-level events (a GitHub connection failing) belong to
-- no project.
--
-- seq is the SSE resume cursor (Last-Event-ID). It is a bigserial rather than a
-- timestamp because two events written in the same millisecond still need a
-- total order a client can resume from.
CREATE TABLE events (
    id           uuid PRIMARY KEY,
    seq          bigserial NOT NULL,
    type         text NOT NULL,
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    project_id   uuid REFERENCES projects(id) ON DELETE CASCADE,
    actor        text,
    payload      jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz
);
-- Outbox scan: unprocessed events in order.
CREATE INDEX idx_events_unprocessed ON events (created_at) WHERE processed_at IS NULL;
-- SSE resume window, per account.
CREATE INDEX idx_events_user_seq ON events (user_id, seq);
CREATE UNIQUE INDEX idx_events_seq ON events (seq);

-- webhooks: per-account delivery targets. Secret encrypted at rest.
CREATE TABLE webhooks (
    id                   uuid PRIMARY KEY,
    user_id              uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    url                  text NOT NULL,
    secret_encrypted     bytea NOT NULL,
    events               jsonb NOT NULL DEFAULT '[]',
    active               boolean NOT NULL DEFAULT true,
    created_at           timestamptz NOT NULL DEFAULT now(),
    disabled_at          timestamptz,
    disabled_reason      text,
    consecutive_failures integer NOT NULL DEFAULT 0
);
CREATE INDEX idx_webhooks_user ON webhooks (user_id);

-- webhook_deliveries: per-attempt delivery log.
CREATE TABLE webhook_deliveries (
    id                    uuid PRIMARY KEY,
    webhook_id            uuid NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
    event_id              uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    attempt               integer NOT NULL DEFAULT 0,
    status                text NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'success', 'failed', 'dead')),
    response_status       integer,
    response_body_snippet text,
    error                 text,
    delivered_at          timestamptz,
    next_retry_at         timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_deliveries_webhook ON webhook_deliveries (webhook_id, created_at DESC);
-- Retry scan: due, not-yet-terminal deliveries.
CREATE INDEX idx_deliveries_due ON webhook_deliveries (next_retry_at)
    WHERE status IN ('pending', 'failed');

-- +goose Down
DROP TABLE webhook_deliveries;
DROP TABLE webhooks;
DROP TABLE events;
