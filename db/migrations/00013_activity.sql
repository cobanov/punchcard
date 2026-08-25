-- +goose Up

-- activity: what happened, in words a person reads. Separate from `events` on
-- purpose. `events` is the sync feed: purged at 30 days, cursored by seq, and
-- free to change shape with the protocol. This is history: kept for 400 days,
-- cursored by time, and its shape is a promise. Folding one into the other
-- would put the delta feed's retention horizon (see changes_test.go) in charge
-- of how far back a person can look, which is not a trade anyone would make on
-- purpose.
CREATE TABLE activity (
    id          uuid PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- Scoping only, and nullable on purpose: deleting a list must not delete
    -- the history of it, or a monthly total would shrink when you tidy up.
    -- The row stays readable through the user_id branch of the read query.
    list_id     uuid REFERENCES lists(id) ON DELETE SET NULL,
    origin      text NOT NULL CHECK (origin IN ('user','agent','mcp','api')),
    action      text NOT NULL,
    -- Snapshots, never joins. A log row is immutable: rename "Work" to "İş" and
    -- last month's entry must still say "Work", because that is what happened.
    subject     text,
    list_name   text,
    detail      jsonb NOT NULL DEFAULT '{}',
    occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_activity_user_time ON activity (user_id, occurred_at DESC, id DESC);
CREATE INDEX idx_activity_list_time ON activity (list_id, occurred_at DESC, id DESC);

-- +goose Down
DROP TABLE activity;
