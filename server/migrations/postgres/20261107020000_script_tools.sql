-- Script tools: file packages registered in a project and mounted
-- read-only into the runs of tasks that reference them (tech design 7.8).
-- Keep in sync with sqlite/20261107020000_script_tools.sql.

-- +goose Up
-- Hex SHA-256 over the sorted file list of a script tool, like
-- task_versions.tree_hash; NULL for http and mcp tools. A run names the
-- tree it mounted in its log.
ALTER TABLE tools ADD COLUMN tree_hash text;

-- The files of a script tool. Contents live in the object store under
-- blobs/sha256/, like task files. Registering the tool again replaces
-- them all.
CREATE TABLE tool_files (
    id         uuid    PRIMARY KEY,
    tool_id    uuid    NOT NULL REFERENCES tools (id) ON DELETE CASCADE,
    path       text    NOT NULL,
    blob_hash  text    NOT NULL,
    size       bigint  NOT NULL,
    executable boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX tool_files_tool_path_key ON tool_files (tool_id, path);

-- +goose Down
DROP TABLE tool_files;
ALTER TABLE tools DROP COLUMN tree_hash;
