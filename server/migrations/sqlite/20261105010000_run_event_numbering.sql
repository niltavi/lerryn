-- Server-side run events for M2 (tech design 5.5). Keep in sync with
-- postgres/20261105010000_run_event_numbering.sql, which documents the
-- columns.

-- +goose Up
ALTER TABLE runs ADD COLUMN runner_event_seq INTEGER NOT NULL DEFAULT 0;
UPDATE runs SET runner_event_seq = event_seq;
CREATE INDEX runs_queued_idx ON runs (queued_at);
CREATE INDEX runs_task_finished_idx ON runs (task_id, finished_at) WHERE finished_at IS NOT NULL;

-- +goose Down
DROP INDEX runs_task_finished_idx;
DROP INDEX runs_queued_idx;
ALTER TABLE runs DROP COLUMN runner_event_seq;
