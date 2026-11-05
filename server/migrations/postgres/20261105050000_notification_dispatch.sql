-- Notification dispatch (tech design 14). Keep in sync with
-- sqlite/20261105050000_notification_dispatch.sql.

-- +goose Up

-- One row per source event that has been turned into deliveries. Every
-- server replica scans for new events; the unique keys make sure each
-- event fans out once, whichever replica sees it first.
CREATE TABLE notification_events (
    id                    uuid        PRIMARY KEY,
    -- A NotificationEventType value.
    event_type            text        NOT NULL,
    -- The run of run_failed, run_timed_out and run_interrupted. Not a
    -- foreign key: the marker must outlive the run's retention.
    run_id                uuid,
    -- The credential and expiry of credential_expiring; a credential whose
    -- expiry moves is announced again.
    credential_id         uuid,
    credential_expires_at timestamptz,
    -- Deliveries the event produced or merged into; 0 when nobody
    -- subscribed or the run was retried.
    deliveries            integer     NOT NULL,
    created_at            timestamptz NOT NULL
);
CREATE UNIQUE INDEX notification_events_run_key ON notification_events (run_id);
CREATE UNIQUE INDEX notification_events_credential_key ON notification_events (credential_id, credential_expires_at);
CREATE INDEX notification_events_created_idx ON notification_events (created_at);

-- When a delivery first became due. A later failure of the same task
-- waits until this plus the merge window, so a task that keeps failing
-- sends at most one notification per window to a channel.
ALTER TABLE notification_deliveries ADD COLUMN scheduled_at timestamptz;

-- The dispatcher looks for runs that ended recently.
CREATE INDEX runs_finished_idx ON runs (finished_at) WHERE finished_at IS NOT NULL;

-- +goose Down
DROP INDEX runs_finished_idx;
ALTER TABLE notification_deliveries DROP COLUMN scheduled_at;
DROP TABLE notification_events;
