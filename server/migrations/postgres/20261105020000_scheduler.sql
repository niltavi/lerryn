-- Scheduler bookkeeping for M3 (tech design 8.4, 8.5). Keep in sync with
-- sqlite/20261105020000_scheduler.sql.

-- +goose Up
-- When the scheduler decided whether a failed attempt is retried: set
-- when the retry run is created and when the run is not retried (status
-- not in retry.on, credential errors). NULL while undecided.
ALTER TABLE runs ADD COLUMN retry_decided_at timestamptz;
-- At most one retry per attempt, even if two leaders decide at once.
CREATE UNIQUE INDEX runs_retry_of_key ON runs (retry_of) WHERE retry_of IS NOT NULL;
-- The retry sweep reads ended attempts that may still be retried.
CREATE INDEX runs_retry_pending_idx ON runs (finished_at)
    WHERE retry_decided_at IS NULL AND attempt < max_attempts;
-- Project concurrency counts a project's claimed runs at claim time.
CREATE INDEX runs_project_status_idx ON runs (project_id, status);

-- +goose Down
DROP INDEX runs_project_status_idx;
DROP INDEX runs_retry_pending_idx;
DROP INDEX runs_retry_of_key;
ALTER TABLE runs DROP COLUMN retry_decided_at;
