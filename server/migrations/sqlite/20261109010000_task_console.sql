-- Console options of a task (issue #32). Keep in sync with
-- postgres/20261109010000_task_console.sql.

-- +goose Up
-- Stored as "hide" so that the zero value, which a task insert writes
-- unless told otherwise, is the default: the manual run button shows.
ALTER TABLE tasks ADD COLUMN console_hide_manual_run INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE tasks DROP COLUMN console_hide_manual_run;
