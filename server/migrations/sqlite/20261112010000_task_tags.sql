-- Keep in sync with postgres/20261112010000_task_tags.sql.

-- +goose Up
CREATE TABLE task_tags (
    task_id TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    tag     TEXT NOT NULL CHECK (length(tag) BETWEEN 1 AND 40),
    PRIMARY KEY (task_id, tag)
) STRICT;
CREATE INDEX task_tags_tag_task_idx ON task_tags (tag, task_id);

-- +goose Down
DROP TABLE task_tags;
