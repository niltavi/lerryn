-- Notification dispatch (tech design 14). Keep in sync with
-- postgres/20261105050000_notification_dispatch.sql, which documents the
-- columns.

-- +goose Up
CREATE TABLE notification_events (
    id                    TEXT    NOT NULL PRIMARY KEY,
    event_type            TEXT    NOT NULL,
    run_id                TEXT,
    credential_id         TEXT,
    credential_expires_at TEXT,
    deliveries            INTEGER NOT NULL,
    created_at            TEXT    NOT NULL
) STRICT;
CREATE UNIQUE INDEX notification_events_run_key ON notification_events (run_id);
CREATE UNIQUE INDEX notification_events_credential_key ON notification_events (credential_id, credential_expires_at);
CREATE INDEX notification_events_created_idx ON notification_events (created_at);

ALTER TABLE notification_deliveries ADD COLUMN scheduled_at TEXT;

CREATE INDEX runs_finished_idx ON runs (finished_at) WHERE finished_at IS NOT NULL;

-- +goose Down
DROP INDEX runs_finished_idx;
ALTER TABLE notification_deliveries DROP COLUMN scheduled_at;
DROP TABLE notification_events;
