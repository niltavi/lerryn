-- Triggers, trigger fires and idempotency records for M2 and M3 (tech
-- design 8, 9.3). Keep in sync with
-- sqlite/20261104011000_triggers_idempotency.sql.

-- +goose Up
CREATE TABLE triggers (
    id             uuid        PRIMARY KEY,
    task_id        uuid        NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    -- Unique within the task; setTriggers matches existing triggers by name.
    name           text        NOT NULL,
    -- cron or webhook.
    type           text        NOT NULL,
    enabled        boolean     NOT NULL DEFAULT true,
    -- cron only: the expression and its IANA time zone.
    schedule       text,
    timezone       text,
    -- skip or latest (tech design 8.2).
    catch_up       text        NOT NULL DEFAULT 'skip',
    -- JSON object replacing the manifest params, NULL to use them.
    params         text,
    -- webhook only: body limit, and the hex SHA-256 and display prefix of
    -- the token. The token itself is never stored.
    max_body_bytes bigint,
    token_hash     text,
    token_prefix   text,
    -- Next scheduled time, NULL while the trigger cannot fire (webhook,
    -- disabled, or task inactive).
    next_fire_at   timestamptz,
    last_fired_at  timestamptz,
    created_by     uuid        NOT NULL REFERENCES users (id),
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL
);
CREATE UNIQUE INDEX triggers_task_name_key ON triggers (task_id, name);
CREATE UNIQUE INDEX triggers_token_hash_key ON triggers (token_hash);
-- The scheduler loop scans due triggers.
CREATE INDEX triggers_next_fire_idx ON triggers (next_fire_at) WHERE next_fire_at IS NOT NULL;

-- One row per scheduled time of a trigger. The unique key is what keeps
-- several servers from creating the same run twice (tech design 8.2).
CREATE TABLE trigger_fires (
    id           uuid        PRIMARY KEY,
    trigger_id   uuid        NOT NULL REFERENCES triggers (id) ON DELETE CASCADE,
    scheduled_at timestamptz NOT NULL,
    -- queued or skipped.
    outcome      text        NOT NULL,
    run_id       uuid        REFERENCES runs (id),
    -- For missed fires collapsed into one skipped record: how many times
    -- were missed, up to scheduled_at.
    missed_count integer     NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL
);
CREATE UNIQUE INDEX trigger_fires_trigger_time_key ON trigger_fires (trigger_id, scheduled_at);

-- Idempotency-Key records (tech design 9.3), kept for 24 hours.
CREATE TABLE idempotency_records (
    id           uuid        PRIMARY KEY,
    -- Who sent the key: user:<id> for tokens and sessions, trigger:<id>
    -- for webhook calls. Keys of different principals never collide.
    principal    text        NOT NULL,
    key          text        NOT NULL,
    -- operationId the key was first used with.
    operation    text        NOT NULL,
    -- Hex SHA-256 over the method, path and body of the first request.
    request_hash text        NOT NULL,
    -- Status and JSON body of the first response; NULL while it is in
    -- progress.
    status       integer,
    response     text,
    created_at   timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL
);
CREATE UNIQUE INDEX idempotency_records_principal_key ON idempotency_records (principal, key);
CREATE INDEX idempotency_records_expires_idx ON idempotency_records (expires_at);

-- +goose Down
DROP TABLE idempotency_records;
DROP TABLE trigger_fires;
DROP TABLE triggers;
