-- Tasks, versions, runs, run events and log chunks for M1 (tech design 5.5
-- and 6.5). Keep in sync with sqlite/20261103010000_tasks_runs.sql.

-- +goose Up
CREATE TABLE tasks (
    id          uuid        PRIMARY KEY,
    project_id  uuid        NOT NULL REFERENCES projects (id),
    name        text        NOT NULL,
    -- From the latest version's manifest, so lists need not parse JSON.
    description text        NOT NULL DEFAULT '',
    latest_seq  integer     NOT NULL,
    created_by  uuid        NOT NULL REFERENCES users (id),
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL
);
CREATE UNIQUE INDEX tasks_project_name_key ON tasks (project_id, name);

CREATE TABLE task_versions (
    id         uuid        PRIMARY KEY,
    task_id    uuid        NOT NULL REFERENCES tasks (id),
    seq        integer     NOT NULL,
    -- JSON: the manifest as submitted. Never queried.
    manifest   text        NOT NULL,
    -- Hex SHA-256 over the sorted file list: path, executable bit and
    -- content hash of every file.
    tree_hash  text        NOT NULL,
    created_by uuid        NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX task_versions_task_seq_key ON task_versions (task_id, seq);

CREATE TABLE task_files (
    id         uuid    PRIMARY KEY,
    version_id uuid    NOT NULL REFERENCES task_versions (id) ON DELETE CASCADE,
    path       text    NOT NULL,
    -- Hex SHA-256 of the content, which lives in the object store under
    -- blobs/sha256/.
    blob_hash  text    NOT NULL,
    size       bigint  NOT NULL,
    executable boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX task_files_version_path_key ON task_files (version_id, path);

-- Text IDs: a runner chooses its own stable ID (runproto.Registration).
CREATE TABLE runners (
    id           text        PRIMARY KEY,
    name         text        NOT NULL,
    slots        integer     NOT NULL,
    last_seen_at timestamptz NOT NULL,
    created_at   timestamptz NOT NULL
);

CREATE TABLE runs (
    id                  uuid        PRIMARY KEY,
    project_id          uuid        NOT NULL REFERENCES projects (id),
    task_id             uuid        NOT NULL REFERENCES tasks (id),
    version_id          uuid        NOT NULL REFERENCES task_versions (id),
    version_seq         integer     NOT NULL,
    -- queued, preparing, running, cancelling, completed, failed,
    -- timed_out, cancelled, interrupted or skipped (tech design 5.4).
    status              text        NOT NULL,
    is_test             boolean     NOT NULL DEFAULT false,
    -- manual in M1; cron and webhook later.
    trigger_kind        text        NOT NULL,
    created_by          uuid        REFERENCES users (id),
    -- JSON object passed to the task.
    params              text        NOT NULL DEFAULT '{}',
    -- The task ID while the task forbids overlapping runs, NULL otherwise;
    -- see runs_concurrency_key.
    concurrency_key     text,
    runner_id           text        REFERENCES runners (id),
    -- Written with the database clock (tech design 4.4).
    lease_expires_at    timestamptz,
    env_id              text,
    cancel_requested_at timestamptz,
    queued_at           timestamptz NOT NULL,
    started_at          timestamptz,
    finished_at         timestamptz,
    exit_code           integer,
    -- An ErrorCode value of the API contract.
    error_code          text,
    error_stage         text,
    error_message       text,
    -- JSON of the last reported result: status, summary, data.
    result              text,
    -- Highest log line number stored; lines are numbered from 1.
    log_lines           bigint      NOT NULL DEFAULT 0,
    dropped_lines       bigint      NOT NULL DEFAULT 0,
    dropped_bytes       bigint      NOT NULL DEFAULT 0,
    -- Highest event number stored.
    event_seq           bigint      NOT NULL DEFAULT 0
);
-- At most one active run per task that forbids overlap. NULL keys are never
-- equal, so tasks that allow overlap are unaffected.
CREATE UNIQUE INDEX runs_concurrency_key ON runs (concurrency_key)
    WHERE concurrency_key IS NOT NULL AND status IN ('queued', 'preparing', 'running', 'cancelling');
CREATE INDEX runs_status_queued_at_idx ON runs (status, queued_at);
CREATE INDEX runs_task_id_idx ON runs (task_id, queued_at);
-- Reconciliation scans active runs by lease expiry.
CREATE INDEX runs_lease_idx ON runs (status, lease_expires_at);

CREATE TABLE run_events (
    id      uuid        PRIMARY KEY,
    run_id  uuid        NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    seq     bigint      NOT NULL,
    ts      timestamptz NOT NULL,
    -- stage.start, stage.done, stage.fail or result.
    kind    text        NOT NULL,
    stage   text,
    message text,
    -- JSON of a result event.
    payload text
);
CREATE UNIQUE INDEX run_events_run_seq_key ON run_events (run_id, seq);

-- Logs of a run as batches of NDJSON lines (tech design 6.5). In M1 they
-- stay here; archiving to the object store comes with M5.
CREATE TABLE log_chunks (
    id         uuid        PRIMARY KEY,
    run_id     uuid        NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    seq_from   bigint      NOT NULL,
    seq_to     bigint      NOT NULL,
    data       text        NOT NULL,
    bytes      bigint      NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX log_chunks_run_seq_key ON log_chunks (run_id, seq_from);

-- +goose Down
DROP TABLE log_chunks;
DROP TABLE run_events;
DROP TABLE runs;
DROP TABLE runners;
DROP TABLE task_files;
DROP TABLE task_versions;
DROP TABLE tasks;
