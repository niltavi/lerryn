-- Scheduler bookkeeping for M3 (tech design 8.4, 8.5). Keep in sync with
-- postgres/20261105020000_scheduler.sql, which documents the columns.

-- +goose Up
ALTER TABLE runs ADD COLUMN retry_decided_at TEXT;
CREATE UNIQUE INDEX runs_retry_of_key ON runs (retry_of) WHERE retry_of IS NOT NULL;
CREATE INDEX runs_retry_pending_idx ON runs (finished_at)
    WHERE retry_decided_at IS NULL AND attempt < max_attempts;
CREATE INDEX runs_project_status_idx ON runs (project_id, status);

-- +goose Down
DROP INDEX runs_project_status_idx;
DROP INDEX runs_retry_pending_idx;
DROP INDEX runs_retry_of_key;
ALTER TABLE runs DROP COLUMN retry_decided_at;
