-- Keep in sync with sqlite/20261113010000_task_title.sql.

-- +goose Up
ALTER TABLE tasks ADD COLUMN title text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE tasks DROP COLUMN title;
