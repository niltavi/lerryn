-- Remote runners, runner labels and log archives for M5 (tech design 4.4,
-- 4.5, 6.5). Keep in sync with sqlite/20261104020000_runners_multinode.sql.

-- +goose Up
-- A remote runner authenticates with a credential it received in exchange
-- for a registration token; token_hash is NULL for the embedded runner of
-- all-in-one, which never talks HTTP. A disabled runner cannot call the
-- server and claims nothing.
ALTER TABLE runners ADD COLUMN token_hash text;
ALTER TABLE runners ADD COLUMN protocol_version integer NOT NULL DEFAULT 1;
ALTER TABLE runners ADD COLUMN disabled_at timestamptz;
-- The release the runner runs, as it reports at registration.
ALTER TABLE runners ADD COLUMN version text;
CREATE UNIQUE INDEX runners_token_hash_key ON runners (token_hash);

CREATE TABLE runner_labels (
    id        uuid PRIMARY KEY,
    runner_id text NOT NULL REFERENCES runners (id) ON DELETE CASCADE,
    label     text NOT NULL
);
CREATE UNIQUE INDEX runner_labels_runner_label_key ON runner_labels (runner_id, label);

-- Labels a run requires from its runner, copied from the manifest's
-- runner_labels when the run is queued.
CREATE TABLE run_required_labels (
    id     uuid PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    label  text NOT NULL
);
CREATE UNIQUE INDEX run_required_labels_run_label_key ON run_required_labels (run_id, label);

-- Registration tokens an admin creates; a runner exchanges one for its own
-- credential. Reusable until it expires or is revoked.
CREATE TABLE runner_tokens (
    id           uuid        PRIMARY KEY,
    name         text        NOT NULL,
    token_hash   text        NOT NULL,
    prefix       text        NOT NULL,
    created_by   uuid        REFERENCES users (id),
    expires_at   timestamptz,
    revoked_at   timestamptz,
    last_used_at timestamptz,
    created_at   timestamptz NOT NULL
);
CREATE UNIQUE INDEX runner_tokens_token_hash_key ON runner_tokens (token_hash);

-- JSON: where a finished run's archived log lives in the object store and
-- the index of its frames. NULL while the log is still in log_chunks.
ALTER TABLE runs ADD COLUMN log_archive text;
-- The leader archives finished runs whose log is still in chunks.
CREATE INDEX runs_log_unarchived_idx ON runs (finished_at)
    WHERE log_archive IS NULL AND finished_at IS NOT NULL;

-- +goose Down
DROP INDEX runs_log_unarchived_idx;
ALTER TABLE runs DROP COLUMN log_archive;
DROP TABLE runner_tokens;
DROP TABLE run_required_labels;
DROP TABLE runner_labels;
DROP INDEX runners_token_hash_key;
ALTER TABLE runners DROP COLUMN version;
ALTER TABLE runners DROP COLUMN disabled_at;
ALTER TABLE runners DROP COLUMN protocol_version;
ALTER TABLE runners DROP COLUMN token_hash;
