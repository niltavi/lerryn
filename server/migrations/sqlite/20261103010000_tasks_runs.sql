-- Tasks, versions, runs, run events and log chunks for M1 (tech design 5.5
-- and 6.5). Keep in sync with postgres/20261103010000_tasks_runs.sql, which
-- documents the columns.

-- +goose Up
CREATE TABLE tasks (
    id          TEXT    NOT NULL PRIMARY KEY,
    project_id  TEXT    NOT NULL REFERENCES projects (id),
    name        TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    latest_seq  INTEGER NOT NULL,
    created_by  TEXT    NOT NULL REFERENCES users (id),
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL
) STRICT;
CREATE UNIQUE INDEX tasks_project_name_key ON tasks (project_id, name);

CREATE TABLE task_versions (
    id         TEXT    NOT NULL PRIMARY KEY,
    task_id    TEXT    NOT NULL REFERENCES tasks (id),
    seq        INTEGER NOT NULL,
    manifest   TEXT    NOT NULL,
    tree_hash  TEXT    NOT NULL,
    created_by TEXT    NOT NULL REFERENCES users (id),
    created_at TEXT    NOT NULL
) STRICT;
CREATE UNIQUE INDEX task_versions_task_seq_key ON task_versions (task_id, seq);

CREATE TABLE task_files (
    id         TEXT    NOT NULL PRIMARY KEY,
    version_id TEXT    NOT NULL REFERENCES task_versions (id) ON DELETE CASCADE,
    path       TEXT    NOT NULL,
    blob_hash  TEXT    NOT NULL,
    size       INTEGER NOT NULL,
    executable INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE UNIQUE INDEX task_files_version_path_key ON task_files (version_id, path);

CREATE TABLE runners (
    id           TEXT    NOT NULL PRIMARY KEY,
    name         TEXT    NOT NULL,
    slots        INTEGER NOT NULL,
    last_seen_at TEXT    NOT NULL,
    created_at   TEXT    NOT NULL
) STRICT;

CREATE TABLE runs (
    id                  TEXT    NOT NULL PRIMARY KEY,
    project_id          TEXT    NOT NULL REFERENCES projects (id),
    task_id             TEXT    NOT NULL REFERENCES tasks (id),
    version_id          TEXT    NOT NULL REFERENCES task_versions (id),
    version_seq         INTEGER NOT NULL,
    status              TEXT    NOT NULL,
    is_test             INTEGER NOT NULL DEFAULT 0,
    trigger_kind        TEXT    NOT NULL,
    created_by          TEXT    REFERENCES users (id),
    params              TEXT    NOT NULL DEFAULT '{}',
    concurrency_key     TEXT,
    runner_id           TEXT    REFERENCES runners (id),
    lease_expires_at    TEXT,
    env_id              TEXT,
    cancel_requested_at TEXT,
    queued_at           TEXT    NOT NULL,
    started_at          TEXT,
    finished_at         TEXT,
    exit_code           INTEGER,
    error_code          TEXT,
    error_stage         TEXT,
    error_message       TEXT,
    result              TEXT,
    log_lines           INTEGER NOT NULL DEFAULT 0,
    dropped_lines       INTEGER NOT NULL DEFAULT 0,
    dropped_bytes       INTEGER NOT NULL DEFAULT 0,
    event_seq           INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE UNIQUE INDEX runs_concurrency_key ON runs (concurrency_key)
    WHERE concurrency_key IS NOT NULL AND status IN ('queued', 'preparing', 'running', 'cancelling');
CREATE INDEX runs_status_queued_at_idx ON runs (status, queued_at);
CREATE INDEX runs_task_id_idx ON runs (task_id, queued_at);
CREATE INDEX runs_lease_idx ON runs (status, lease_expires_at);

CREATE TABLE run_events (
    id      TEXT    NOT NULL PRIMARY KEY,
    run_id  TEXT    NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    seq     INTEGER NOT NULL,
    ts      TEXT    NOT NULL,
    kind    TEXT    NOT NULL,
    stage   TEXT,
    message TEXT,
    payload TEXT
) STRICT;
CREATE UNIQUE INDEX run_events_run_seq_key ON run_events (run_id, seq);

CREATE TABLE log_chunks (
    id         TEXT    NOT NULL PRIMARY KEY,
    run_id     TEXT    NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    seq_from   INTEGER NOT NULL,
    seq_to     INTEGER NOT NULL,
    data       TEXT    NOT NULL,
    bytes      INTEGER NOT NULL,
    created_at TEXT    NOT NULL
) STRICT;
CREATE UNIQUE INDEX log_chunks_run_seq_key ON log_chunks (run_id, seq_from);

-- +goose Down
DROP TABLE log_chunks;
DROP TABLE run_events;
DROP TABLE runs;
DROP TABLE runners;
DROP TABLE task_files;
DROP TABLE task_versions;
DROP TABLE tasks;
