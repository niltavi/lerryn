-- Script tools: file packages registered in a project and mounted
-- read-only into the runs of tasks that reference them (tech design 7.8).
-- Keep in sync with postgres/20261107020000_script_tools.sql, which
-- documents the columns.

-- +goose Up
ALTER TABLE tools ADD COLUMN tree_hash TEXT;

CREATE TABLE tool_files (
    id         TEXT    NOT NULL PRIMARY KEY,
    tool_id    TEXT    NOT NULL REFERENCES tools (id) ON DELETE CASCADE,
    path       TEXT    NOT NULL,
    blob_hash  TEXT    NOT NULL,
    size       INTEGER NOT NULL,
    executable INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE UNIQUE INDEX tool_files_tool_path_key ON tool_files (tool_id, path);

-- +goose Down
DROP TABLE tool_files;
ALTER TABLE tools DROP COLUMN tree_hash;
