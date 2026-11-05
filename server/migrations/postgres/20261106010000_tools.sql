-- Project tool registry (tech design 7.8). Keep in sync with
-- sqlite/20261106010000_tools.sql.

-- +goose Up
CREATE TABLE tools (
    id          uuid        PRIMARY KEY,
    project_id  uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- Unique within the project; task manifests reference tools by it.
    -- Lowercase letters, digits and '-', so it compares the same on both
    -- databases.
    name        text        NOT NULL,
    -- http (an HTTP API) or mcp (an external MCP server).
    kind        text        NOT NULL,
    description text        NOT NULL,
    -- Base URL handed to the task's container. Credentials are not part
    -- of a tool; the task references them like any other credential.
    url         text        NOT NULL,
    created_by  uuid        NOT NULL REFERENCES users (id),
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL
);
CREATE UNIQUE INDEX tools_project_name_key ON tools (project_id, name);

-- +goose Down
DROP TABLE tools;
