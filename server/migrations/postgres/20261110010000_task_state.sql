-- Cross-run task state and the input/output references held by a run.
-- Keep in sync with sqlite/20261110010000_task_state.sql.

-- +goose Up
CREATE TABLE task_states (
    task_id     uuid        PRIMARY KEY REFERENCES tasks (id) ON DELETE CASCADE,
    blob_hash   text        NOT NULL,
    size        bigint      NOT NULL CHECK (size >= 0 AND size <= 10485760),
    updated_at  timestamptz NOT NULL
);
CREATE INDEX task_states_blob_hash_idx ON task_states (blob_hash);

-- An input row is also the marker that a run must deliver state before
-- completing. Its NULL hash means there was no state when it was claimed.
CREATE TABLE run_state_refs (
    run_id      uuid    NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    role        text    NOT NULL CHECK (role IN ('input', 'output')),
    blob_hash   text,
    size        bigint  NOT NULL CHECK (size >= 0 AND size <= 10485760),
    ready       boolean NOT NULL,
    PRIMARY KEY (run_id, role),
    CHECK (blob_hash IS NOT NULL OR (role = 'input' AND size = 0)),
    CHECK (role <> 'input' OR ready)
);
CREATE INDEX run_state_refs_blob_hash_idx ON run_state_refs (blob_hash) WHERE blob_hash IS NOT NULL;

-- +goose Down
DROP TABLE run_state_refs;
DROP TABLE task_states;
