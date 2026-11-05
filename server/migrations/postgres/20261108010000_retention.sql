-- Retention of run records, logs and artifacts, and collection of the
-- objects they no longer reference (tech design 6.6). Keep in sync with
-- sqlite/20261108010000_retention.sql.

-- +goose Up
-- Set when retention removed the run's log; the run reads as "log
-- expired" rather than as an empty log.
ALTER TABLE runs ADD COLUMN logs_expired_at timestamptz;
-- The hash and size of the log archive object, copied out of the
-- log_archive JSON so that collection can ask whether an object is still
-- referenced and the size cap can add archive sizes up. log_bytes is NULL
-- for a log archived before this migration; the leader fills both in.
ALTER TABLE runs ADD COLUMN log_blob text;
ALTER TABLE runs ADD COLUMN log_bytes bigint;
CREATE INDEX runs_log_blob_idx ON runs (log_blob) WHERE log_blob IS NOT NULL;
CREATE INDEX runs_input_key_idx ON runs (input_key) WHERE input_key IS NOT NULL;
-- Log expiry by age and by total size picks the oldest kept logs.
CREATE INDEX runs_log_kept_idx ON runs (finished_at)
    WHERE logs_expired_at IS NULL AND finished_at IS NOT NULL;

-- Collection asks whether any row still references an object.
CREATE INDEX task_files_blob_hash_idx ON task_files (blob_hash);
CREATE INDEX tool_files_blob_hash_idx ON tool_files (blob_hash);
CREATE INDEX artifacts_blob_key_idx ON artifacts (blob_key) WHERE expired_at IS NULL;
CREATE INDEX artifacts_kept_idx ON artifacts (created_at) WHERE expired_at IS NULL;
-- Deleting a run clears the fires that point at it.
CREATE INDEX trigger_fires_run_id_idx ON trigger_fires (run_id) WHERE run_id IS NOT NULL;

-- Objects that retention stopped referencing. The collector deletes an
-- object once nothing references it, and keeps the row with deleted_at
-- set for a while as a tombstone: a writer that stored the same content
-- just before the deletion finds the tombstone and stores it again.
CREATE TABLE blob_releases (
    id          uuid        PRIMARY KEY,
    hash        text        NOT NULL,
    released_at timestamptz NOT NULL,
    deleted_at  timestamptz
);
CREATE UNIQUE INDEX blob_releases_hash_key ON blob_releases (hash);
CREATE INDEX blob_releases_pending_idx ON blob_releases (released_at) WHERE deleted_at IS NULL;

-- +goose Down
DROP TABLE blob_releases;
DROP INDEX trigger_fires_run_id_idx;
DROP INDEX artifacts_kept_idx;
DROP INDEX artifacts_blob_key_idx;
DROP INDEX tool_files_blob_hash_idx;
DROP INDEX task_files_blob_hash_idx;
DROP INDEX runs_log_kept_idx;
DROP INDEX runs_input_key_idx;
DROP INDEX runs_log_blob_idx;
ALTER TABLE runs DROP COLUMN log_bytes;
ALTER TABLE runs DROP COLUMN log_blob;
ALTER TABLE runs DROP COLUMN logs_expired_at;
