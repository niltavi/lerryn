-- Triggers, trigger fires and idempotency records for M2 and M3 (tech
-- design 8, 9.3). Keep in sync with
-- postgres/20261104011000_triggers_idempotency.sql, which documents the
-- columns.

-- +goose Up
CREATE TABLE triggers (
    id             TEXT    NOT NULL PRIMARY KEY,
    task_id        TEXT    NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    name           TEXT    NOT NULL,
    type           TEXT    NOT NULL,
    enabled        INTEGER NOT NULL DEFAULT 1,
    schedule       TEXT,
    timezone       TEXT,
    catch_up       TEXT    NOT NULL DEFAULT 'skip',
    params         TEXT,
    max_body_bytes INTEGER,
    token_hash     TEXT,
    token_prefix   TEXT,
    next_fire_at   TEXT,
    last_fired_at  TEXT,
    created_by     TEXT    NOT NULL REFERENCES users (id),
    created_at     TEXT    NOT NULL,
    updated_at     TEXT    NOT NULL
) STRICT;
CREATE UNIQUE INDEX triggers_task_name_key ON triggers (task_id, name);
CREATE UNIQUE INDEX triggers_token_hash_key ON triggers (token_hash);
CREATE INDEX triggers_next_fire_idx ON triggers (next_fire_at) WHERE next_fire_at IS NOT NULL;

CREATE TABLE trigger_fires (
    id           TEXT    NOT NULL PRIMARY KEY,
    trigger_id   TEXT    NOT NULL REFERENCES triggers (id) ON DELETE CASCADE,
    scheduled_at TEXT    NOT NULL,
    outcome      TEXT    NOT NULL,
    run_id       TEXT    REFERENCES runs (id),
    missed_count INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT    NOT NULL
) STRICT;
CREATE UNIQUE INDEX trigger_fires_trigger_time_key ON trigger_fires (trigger_id, scheduled_at);

CREATE TABLE idempotency_records (
    id           TEXT    NOT NULL PRIMARY KEY,
    principal    TEXT    NOT NULL,
    key          TEXT    NOT NULL,
    operation    TEXT    NOT NULL,
    request_hash TEXT    NOT NULL,
    status       INTEGER,
    response     TEXT,
    created_at   TEXT    NOT NULL,
    expires_at   TEXT    NOT NULL
) STRICT;
CREATE UNIQUE INDEX idempotency_records_principal_key ON idempotency_records (principal, key);
CREATE INDEX idempotency_records_expires_idx ON idempotency_records (expires_at);

-- +goose Down
DROP TABLE idempotency_records;
DROP TABLE trigger_fires;
DROP TABLE triggers;
