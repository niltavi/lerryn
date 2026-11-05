-- Project tool registry (tech design 7.8). Keep in sync with
-- postgres/20261106010000_tools.sql, which documents the columns.

-- +goose Up
CREATE TABLE tools (
    id          TEXT NOT NULL PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL,
    description TEXT NOT NULL,
    url         TEXT NOT NULL,
    created_by  TEXT NOT NULL REFERENCES users (id),
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX tools_project_name_key ON tools (project_id, name);

-- +goose Down
DROP TABLE tools;
