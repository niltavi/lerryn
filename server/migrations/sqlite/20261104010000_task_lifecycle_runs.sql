-- Task activation and archiving, version provenance, and run provenance,
-- retries and reruns for M2 and M3 (tech design 5.2, 5.4, 8.5). Keep in
-- sync with postgres/20261104010000_task_lifecycle_runs.sql, which
-- documents the columns.
--
-- Columns added here carry no REFERENCES clause: SQLite cannot drop a
-- column that takes part in a foreign key, so the down migration would
-- fail. PostgreSQL has the foreign keys; the application keeps the
-- references valid on both.

-- +goose Up
ALTER TABLE tasks ADD COLUMN state TEXT NOT NULL DEFAULT 'inactive';
ALTER TABLE tasks ADD COLUMN active_version_id TEXT;
ALTER TABLE tasks ADD COLUMN active_seq INTEGER;
ALTER TABLE tasks ADD COLUMN activated_at TEXT;
ALTER TABLE tasks ADD COLUMN archived_at TEXT;
ALTER TABLE tasks ADD COLUMN triggers_revision INTEGER NOT NULL DEFAULT 0;
DROP INDEX tasks_project_name_key;
CREATE UNIQUE INDEX tasks_project_name_key ON tasks (project_id, name) WHERE archived_at IS NULL;
CREATE INDEX tasks_project_updated_idx ON tasks (project_id, updated_at);

ALTER TABLE task_versions ADD COLUMN client TEXT;
ALTER TABLE task_versions ADD COLUMN tested_at TEXT;

ALTER TABLE runs ADD COLUMN trigger_id TEXT;
ALTER TABLE runs ADD COLUMN trigger_fire_id TEXT;
ALTER TABLE runs ADD COLUMN scheduled_at TEXT;
ALTER TABLE runs ADD COLUMN client TEXT;
ALTER TABLE runs ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1;
ALTER TABLE runs ADD COLUMN max_attempts INTEGER NOT NULL DEFAULT 1;
ALTER TABLE runs ADD COLUMN retry_of TEXT;
ALTER TABLE runs ADD COLUMN not_before TEXT;
ALTER TABLE runs ADD COLUMN rerun_of TEXT;
ALTER TABLE runs ADD COLUMN skip_reason TEXT;
ALTER TABLE runs ADD COLUMN claimed_at TEXT;
ALTER TABLE runs ADD COLUMN runner_name TEXT;
ALTER TABLE runs ADD COLUMN cancel_requested_by TEXT;
ALTER TABLE runs ADD COLUMN cancel_client TEXT;
ALTER TABLE runs ADD COLUMN input_key TEXT;
ALTER TABLE runs ADD COLUMN input_content_type TEXT;
CREATE INDEX runs_project_queued_idx ON runs (project_id, queued_at);
CREATE INDEX runs_runner_queued_idx ON runs (runner_id, queued_at);
CREATE INDEX runs_rerun_of_idx ON runs (rerun_of) WHERE rerun_of IS NOT NULL;

-- +goose Down
DROP INDEX runs_rerun_of_idx;
DROP INDEX runs_runner_queued_idx;
DROP INDEX runs_project_queued_idx;
ALTER TABLE runs DROP COLUMN input_content_type;
ALTER TABLE runs DROP COLUMN input_key;
ALTER TABLE runs DROP COLUMN cancel_client;
ALTER TABLE runs DROP COLUMN cancel_requested_by;
ALTER TABLE runs DROP COLUMN runner_name;
ALTER TABLE runs DROP COLUMN claimed_at;
ALTER TABLE runs DROP COLUMN skip_reason;
ALTER TABLE runs DROP COLUMN rerun_of;
ALTER TABLE runs DROP COLUMN not_before;
ALTER TABLE runs DROP COLUMN retry_of;
ALTER TABLE runs DROP COLUMN max_attempts;
ALTER TABLE runs DROP COLUMN attempt;
ALTER TABLE runs DROP COLUMN client;
ALTER TABLE runs DROP COLUMN scheduled_at;
ALTER TABLE runs DROP COLUMN trigger_fire_id;
ALTER TABLE runs DROP COLUMN trigger_id;
ALTER TABLE task_versions DROP COLUMN tested_at;
ALTER TABLE task_versions DROP COLUMN client;
DROP INDEX tasks_project_updated_idx;
DROP INDEX tasks_project_name_key;
CREATE UNIQUE INDEX tasks_project_name_key ON tasks (project_id, name);
ALTER TABLE tasks DROP COLUMN triggers_revision;
ALTER TABLE tasks DROP COLUMN archived_at;
ALTER TABLE tasks DROP COLUMN activated_at;
ALTER TABLE tasks DROP COLUMN active_seq;
ALTER TABLE tasks DROP COLUMN active_version_id;
ALTER TABLE tasks DROP COLUMN state;
