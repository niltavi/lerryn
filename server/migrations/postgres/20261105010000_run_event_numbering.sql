-- Server-side run events for M2 (tech design 5.5): status changes,
-- artifacts and log truncation are numbered in the same sequence as the
-- events a runner reports, so listRunEvents pages through one ordered
-- list. Keep in sync with sqlite/20261105010000_run_event_numbering.sql.

-- +goose Up
-- The last event number the runner sent. Runner batches are deduplicated
-- against it; event_seq stays the last number stored for the run.
ALTER TABLE runs ADD COLUMN runner_event_seq bigint NOT NULL DEFAULT 0;
UPDATE runs SET runner_event_seq = event_seq;
-- listRuns filters and orders by these.
CREATE INDEX runs_queued_idx ON runs (queued_at);
CREATE INDEX runs_task_finished_idx ON runs (task_id, finished_at) WHERE finished_at IS NOT NULL;

-- +goose Down
DROP INDEX runs_task_finished_idx;
DROP INDEX runs_queued_idx;
ALTER TABLE runs DROP COLUMN runner_event_seq;
