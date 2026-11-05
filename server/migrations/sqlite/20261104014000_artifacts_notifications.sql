-- Run artifacts (tech design 6.4, 7.4) and notification channels,
-- subscriptions and deliveries (tech design 14). Keep in sync with
-- postgres/20261104014000_artifacts_notifications.sql, which documents the
-- columns.

-- +goose Up
CREATE TABLE artifacts (
    id           TEXT    NOT NULL PRIMARY KEY,
    run_id       TEXT    NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    path         TEXT    NOT NULL,
    title        TEXT,
    size         INTEGER NOT NULL,
    content_type TEXT    NOT NULL,
    sha256       TEXT    NOT NULL,
    blob_key     TEXT    NOT NULL,
    created_at   TEXT    NOT NULL,
    expired_at   TEXT
) STRICT;
CREATE UNIQUE INDEX artifacts_run_path_key ON artifacts (run_id, path);

CREATE TABLE notification_channels (
    id                 TEXT    NOT NULL PRIMARY KEY,
    owner_user_id      TEXT    NOT NULL REFERENCES users (id),
    name               TEXT    NOT NULL,
    kind               TEXT    NOT NULL,
    enabled            INTEGER NOT NULL DEFAULT 1,
    url_preview        TEXT    NOT NULL,
    secret_ciphertext  BLOB    NOT NULL,
    secret_wrapped_dek BLOB    NOT NULL,
    secret_kek_id      TEXT    NOT NULL,
    created_at         TEXT    NOT NULL,
    updated_at         TEXT    NOT NULL
) STRICT;
CREATE INDEX notification_channels_owner_idx ON notification_channels (owner_user_id);

CREATE TABLE notification_subscriptions (
    id         TEXT NOT NULL PRIMARY KEY,
    channel_id TEXT NOT NULL REFERENCES notification_channels (id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    task_id    TEXT REFERENCES tasks (id) ON DELETE CASCADE,
    events     TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;
CREATE INDEX notification_subscriptions_channel_idx ON notification_subscriptions (channel_id);
CREATE INDEX notification_subscriptions_project_idx ON notification_subscriptions (project_id, task_id);

CREATE TABLE notification_deliveries (
    id              TEXT    NOT NULL PRIMARY KEY,
    channel_id      TEXT    NOT NULL REFERENCES notification_channels (id) ON DELETE CASCADE,
    event_type      TEXT    NOT NULL,
    merge_key       TEXT,
    task_id         TEXT,
    run_id          TEXT,
    merged_count    INTEGER NOT NULL DEFAULT 1,
    payload         TEXT    NOT NULL,
    status          TEXT    NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    http_status     INTEGER,
    error           TEXT,
    next_attempt_at TEXT,
    last_attempt_at TEXT,
    delivered_at    TEXT,
    created_at      TEXT    NOT NULL
) STRICT;
CREATE INDEX notification_deliveries_channel_idx ON notification_deliveries (channel_id, created_at);
CREATE INDEX notification_deliveries_due_idx ON notification_deliveries (status, next_attempt_at);
CREATE INDEX notification_deliveries_merge_idx ON notification_deliveries (channel_id, merge_key) WHERE status = 'pending';

-- +goose Down
DROP TABLE notification_deliveries;
DROP TABLE notification_subscriptions;
DROP TABLE notification_channels;
DROP TABLE artifacts;
