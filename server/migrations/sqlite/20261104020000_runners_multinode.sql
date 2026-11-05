-- Remote runners, runner labels and log archives for M5 (tech design 4.4,
-- 4.5, 6.5). Keep in sync with postgres/20261104020000_runners_multinode.sql.

-- +goose Up
-- A remote runner authenticates with a credential it received in exchange
-- for a registration token; token_hash is NULL for the embedded runner of
-- all-in-one, which never talks HTTP. A disabled runner cannot call the
-- server and claims nothing.
ALTER TABLE runners ADD COLUMN token_hash TEXT;
ALTER TABLE runners ADD COLUMN protocol_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE runners ADD COLUMN disabled_at TEXT;
-- The release the runner runs, as it reports at registration.
ALTER TABLE runners ADD COLUMN version TEXT;
CREATE UNIQUE INDEX runners_token_hash_key ON runners (token_hash);

CREATE TABLE runner_labels (
    id        TEXT NOT NULL PRIMARY KEY,
    runner_id TEXT NOT NULL REFERENCES runners (id) ON DELETE CASCADE,
    label     TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX runner_labels_runner_label_key ON runner_labels (runner_id, label);

-- Labels a run requires from its runner, copied from the manifest's
-- runner_labels when the run is queued.
CREATE TABLE run_required_labels (
    id     TEXT NOT NULL PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    label  TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX run_required_labels_run_label_key ON run_required_labels (run_id, label);

-- Registration tokens an admin creates; a runner exchanges one for its own
-- credential. Reusable until it expires or is revoked.
CREATE TABLE runner_tokens (
    id           TEXT NOT NULL PRIMARY KEY,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL,
    prefix       TEXT NOT NULL,
    created_by   TEXT REFERENCES users (id),
    expires_at   TEXT,
    revoked_at   TEXT,
    last_used_at TEXT,
    created_at   TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX runner_tokens_token_hash_key ON runner_tokens (token_hash);

-- JSON: where a finished run's archived log lives in the object store and
-- the index of its frames. NULL while the log is still in log_chunks.
ALTER TABLE runs ADD COLUMN log_archive TEXT;
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
