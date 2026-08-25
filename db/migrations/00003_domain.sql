-- +goose Up

-- lists: a todo list. A personal list (is_personal) is one whose only member is
-- its owner; every user gets an undeletable "Inbox" at signup.
CREATE TABLE lists (
    id          uuid PRIMARY KEY,
    name        text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    owner_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    is_personal boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    deleted_at  timestamptz
);
CREATE INDEX idx_lists_owner ON lists (owner_id) WHERE deleted_at IS NULL;

-- list_members: membership + role is the ONLY sharing/authorization
-- primitive. Every task/list access is scoped through this table.
CREATE TABLE list_members (
    list_id    uuid NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       text NOT NULL CHECK (role IN ('owner', 'editor', 'viewer')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (list_id, user_id)
);
-- Serves "list all lists a user belongs to".
CREATE INDEX idx_list_members_user ON list_members (user_id);

-- invites: email-based, single-use, hashed, 7-day expiry.
CREATE TABLE invites (
    id         uuid PRIMARY KEY,
    list_id    uuid NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    email      citext NOT NULL,
    role       text NOT NULL CHECK (role IN ('owner', 'editor', 'viewer')),
    token_hash bytea NOT NULL UNIQUE,
    invited_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    accepted_at timestamptz
);
-- One active (unaccepted) invite per (list, email): re-invite replaces the old.
CREATE UNIQUE INDEX idx_invites_list_email_pending ON invites (list_id, email) WHERE accepted_at IS NULL;
-- Serves "my pending invites" (by email) and quota counting (by inviter).
CREATE INDEX idx_invites_email ON invites (email) WHERE accepted_at IS NULL;
CREATE INDEX idx_invites_inviter ON invites (invited_by) WHERE accepted_at IS NULL;

-- tasks: the core resource. One level of nesting (a subtask has parent_id set
-- and cannot itself be a parent; enforced in the service layer).
CREATE TABLE tasks (
    id           uuid PRIMARY KEY,
    list_id      uuid NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    title        text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 500),
    body         text NOT NULL DEFAULT '' CHECK (char_length(body) <= 16384),
    status       text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'done')),
    due_at       timestamptz,
    priority     smallint NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 3),
    tags         jsonb NOT NULL DEFAULT '[]',
    parent_id    uuid REFERENCES tasks(id) ON DELETE CASCADE,
    position     text NOT NULL DEFAULT '',
    created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    deleted_at   timestamptz
);
-- Hot query: tasks by list, filtered by status, live rows only, sorted by position.
CREATE INDEX idx_tasks_list_status ON tasks (list_id, status, position) WHERE deleted_at IS NULL;
-- updated_since polling for agents.
CREATE INDEX idx_tasks_list_updated ON tasks (list_id, updated_at);
-- Upcoming/overdue open tasks.
CREATE INDEX idx_tasks_due ON tasks (due_at) WHERE status = 'open' AND deleted_at IS NULL;
-- Tag filtering.
CREATE INDEX idx_tasks_tags ON tasks USING GIN (tags);
-- Subtask lookup + purge scan.
CREATE INDEX idx_tasks_parent ON tasks (parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX idx_tasks_deleted ON tasks (deleted_at) WHERE deleted_at IS NOT NULL;

-- idempotency_keys: replay protection for POSTs, 24h TTL purged by the
-- janitor (JANITOR_INTERVAL). Scoped per user.
CREATE TABLE idempotency_keys (
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key           text NOT NULL,
    request_hash  bytea NOT NULL,
    response_status integer NOT NULL,
    response_body bytea NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key)
);
CREATE INDEX idx_idempotency_created ON idempotency_keys (created_at);

-- +goose Down
DROP TABLE idempotency_keys;
DROP TABLE tasks;
DROP TABLE invites;
DROP TABLE list_members;
DROP TABLE lists;
