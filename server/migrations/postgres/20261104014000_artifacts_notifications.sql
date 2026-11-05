-- Run artifacts (tech design 6.4, 7.4) and notification channels,
-- subscriptions and deliveries (tech design 14). Keep in sync with
-- sqlite/20261104014000_artifacts_notifications.sql.

-- +goose Up
CREATE TABLE artifacts (
    id           uuid        PRIMARY KEY,
    run_id       uuid        NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    -- Relative to /sbx/out.
    path         text        NOT NULL,
    title        text,
    size         bigint      NOT NULL,
    content_type text        NOT NULL,
    sha256       text        NOT NULL,
    -- Object store key under artifacts/<run_id>/.
    blob_key     text        NOT NULL,
    created_at   timestamptz NOT NULL,
    -- Set when retention removed the content; the row stays so the
    -- artifact reads as expired rather than missing (tech design 6.6).
    expired_at   timestamptz
);
CREATE UNIQUE INDEX artifacts_run_path_key ON artifacts (run_id, path);

-- A user's notification outlet. The target URL may embed a bot token, so
-- it is stored encrypted like a credential value, together with the
-- generated signing secret of generic webhooks.
CREATE TABLE notification_channels (
    id             uuid        PRIMARY KEY,
    owner_user_id  uuid        NOT NULL REFERENCES users (id),
    name           text        NOT NULL,
    -- webhook, feishu or slack.
    kind           text        NOT NULL,
    enabled        boolean     NOT NULL DEFAULT true,
    -- Masked URL for display.
    url_preview    text        NOT NULL,
    -- AES-256-GCM over the JSON {url, signing_secret}; see credential_versions.
    secret_ciphertext  bytea   NOT NULL,
    secret_wrapped_dek bytea   NOT NULL,
    secret_kek_id      text    NOT NULL,
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL
);
CREATE INDEX notification_channels_owner_idx ON notification_channels (owner_user_id);

CREATE TABLE notification_subscriptions (
    id         uuid        PRIMARY KEY,
    channel_id uuid        NOT NULL REFERENCES notification_channels (id) ON DELETE CASCADE,
    project_id uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- NULL subscribes to the whole project.
    task_id    uuid        REFERENCES tasks (id) ON DELETE CASCADE,
    -- JSON array of event types.
    events     text        NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX notification_subscriptions_channel_idx ON notification_subscriptions (channel_id);
CREATE INDEX notification_subscriptions_project_idx ON notification_subscriptions (project_id, task_id);

-- One notification per channel; consecutive failures of a task merge into
-- the pending row with the same merge_key (tech design 14).
CREATE TABLE notification_deliveries (
    id              uuid        PRIMARY KEY,
    channel_id      uuid        NOT NULL REFERENCES notification_channels (id) ON DELETE CASCADE,
    -- A NotificationEventKind value.
    event_type      text        NOT NULL,
    merge_key       text,
    task_id         uuid,
    run_id          uuid,
    merged_count    integer     NOT NULL DEFAULT 1,
    -- JSON NotificationEvent sent to the channel; never holds secrets.
    payload         text        NOT NULL,
    -- pending, delivered or failed.
    status          text        NOT NULL,
    attempts        integer     NOT NULL DEFAULT 0,
    http_status     integer,
    error           text,
    next_attempt_at timestamptz,
    last_attempt_at timestamptz,
    delivered_at    timestamptz,
    created_at      timestamptz NOT NULL
);
CREATE INDEX notification_deliveries_channel_idx ON notification_deliveries (channel_id, created_at);
CREATE INDEX notification_deliveries_due_idx ON notification_deliveries (status, next_attempt_at);
CREATE INDEX notification_deliveries_merge_idx ON notification_deliveries (channel_id, merge_key) WHERE status = 'pending';

-- +goose Down
DROP TABLE notification_deliveries;
DROP TABLE notification_subscriptions;
DROP TABLE notification_channels;
DROP TABLE artifacts;
