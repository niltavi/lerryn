-- Keep in sync with sqlite/20261112010000_task_tags.sql.

-- +goose Up
CREATE TABLE task_tags (
    task_id uuid NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    tag     text NOT NULL CHECK (length(tag) BETWEEN 1 AND 40),
    PRIMARY KEY (task_id, tag)
);
CREATE INDEX task_tags_tag_task_idx ON task_tags (tag, task_id);

-- +goose Down
DROP TABLE task_tags;
