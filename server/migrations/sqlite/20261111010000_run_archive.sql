-- User-managed run archives. Keep in sync with the PostgreSQL migration.
-- +goose Up
ALTER TABLE runs ADD COLUMN archived_at TEXT;
CREATE INDEX runs_archived_list_idx ON runs (archived_at, queued_at, id);

-- +goose Down
DROP INDEX runs_archived_list_idx;
ALTER TABLE runs DROP COLUMN archived_at;
