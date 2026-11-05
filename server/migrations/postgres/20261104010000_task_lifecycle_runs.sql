-- Task activation and archiving, version provenance, and run provenance,
-- retries and reruns for M2 and M3 (tech design 5.2, 5.4, 8.5). Keep in
-- sync with sqlite/20261104010000_task_lifecycle_runs.sql.

-- +goose Up
-- active or inactive (paused or never activated).
ALTER TABLE tasks ADD COLUMN state text NOT NULL DEFAULT 'inactive';
-- The version triggers run; NULL until the first activation. No foreign
-- key: task_versions already references tasks, and the cycle would stop
-- export and import from ordering the tables. SQLite has none either.
ALTER TABLE tasks ADD COLUMN active_version_id uuid;
ALTER TABLE tasks ADD COLUMN active_seq integer;
ALTER TABLE tasks ADD COLUMN activated_at timestamptz;
-- Archived tasks keep their history but free their name.
ALTER TABLE tasks ADD COLUMN archived_at timestamptz;
-- Bumped by every setTriggers; the If-Match value of the trigger set.
ALTER TABLE tasks ADD COLUMN triggers_revision integer NOT NULL DEFAULT 0;
DROP INDEX tasks_project_name_key;
CREATE UNIQUE INDEX tasks_project_name_key ON tasks (project_id, name) WHERE archived_at IS NULL;
CREATE INDEX tasks_project_updated_idx ON tasks (project_id, updated_at);

-- Name of the access token the version was submitted with, NULL for sessions.
ALTER TABLE task_versions ADD COLUMN client text;
-- End of the first successful test run of the version (tech design 5.2).
ALTER TABLE task_versions ADD COLUMN tested_at timestamptz;

-- The trigger and fire behind cron and webhook runs. Not foreign keys:
-- setTriggers deletes triggers, and runs keep their history.
ALTER TABLE runs ADD COLUMN trigger_id uuid;
ALTER TABLE runs ADD COLUMN trigger_fire_id uuid;
ALTER TABLE runs ADD COLUMN scheduled_at timestamptz;
-- Name of the access token a manual run was started with.
ALTER TABLE runs ADD COLUMN client text;
-- Retries (tech design 8.5): attempt counts from 1; retry_of is the
-- previous attempt. A retry waits in the queue until not_before.
ALTER TABLE runs ADD COLUMN attempt integer NOT NULL DEFAULT 1;
ALTER TABLE runs ADD COLUMN max_attempts integer NOT NULL DEFAULT 1;
ALTER TABLE runs ADD COLUMN retry_of uuid REFERENCES runs (id);
ALTER TABLE runs ADD COLUMN not_before timestamptz;
ALTER TABLE runs ADD COLUMN rerun_of uuid REFERENCES runs (id);
-- overlap, queue_full, missed or inactive for skipped runs.
ALTER TABLE runs ADD COLUMN skip_reason text;
-- When a runner claimed the run; runner_name is copied at claim time so
-- the run still names its runner after the runner is gone.
ALTER TABLE runs ADD COLUMN claimed_at timestamptz;
ALTER TABLE runs ADD COLUMN runner_name text;
ALTER TABLE runs ADD COLUMN cancel_requested_by uuid REFERENCES users (id);
ALTER TABLE runs ADD COLUMN cancel_client text;
-- Object store key and media type of a webhook request body (tech design 8.3).
ALTER TABLE runs ADD COLUMN input_key text;
ALTER TABLE runs ADD COLUMN input_content_type text;
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
