-- Cross-run task state and the input/output references held by a run.
-- Keep in sync with postgres/20261110010000_task_state.sql.

-- +goose Up
CREATE TABLE task_states (
    task_id     TEXT    NOT NULL PRIMARY KEY REFERENCES tasks (id) ON DELETE CASCADE,
    blob_hash   TEXT    NOT NULL,
    size        INTEGER NOT NULL CHECK (size >= 0 AND size <= 10485760),
    updated_at  TEXT    NOT NULL
) STRICT;
CREATE INDEX task_states_blob_hash_idx ON task_states (blob_hash);

-- An input row is also the marker that a run must deliver state before
-- completing. Its NULL hash means there was no state when it was claimed.
CREATE TABLE run_state_refs (
    run_id      TEXT    NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    role        TEXT    NOT NULL CHECK (role IN ('input', 'output')),
    blob_hash   TEXT,
    size        INTEGER NOT NULL CHECK (size >= 0 AND size <= 10485760),
    ready       INTEGER NOT NULL CHECK (ready IN (0, 1)),
    PRIMARY KEY (run_id, role),
    CHECK (blob_hash IS NOT NULL OR (role = 'input' AND size = 0)),
    CHECK (role <> 'input' OR ready = 1)
) STRICT;
CREATE INDEX run_state_refs_blob_hash_idx ON run_state_refs (blob_hash) WHERE blob_hash IS NOT NULL;

-- +goose Down
DROP TABLE run_state_refs;
DROP TABLE task_states;
