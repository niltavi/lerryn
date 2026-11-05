-- Initial schema: accounts, projects, audit and leases (tech design 5.5).
-- Keep in sync with postgres/20261102120000_initial_schema.sql.
--
-- SQLite specifics:
--   * STRICT tables, so a value of the wrong type fails as it would on
--     PostgreSQL instead of being stored silently.
--   * uuid -> TEXT (canonical lowercase form), boolean -> INTEGER 0/1,
--     timestamptz -> TEXT written by Bun as UTC
--     "2006-01-02 15:04:05.999999+00:00", which sorts and compares
--     correctly as text.
--   * NOT NULL is spelled out on primary keys: SQLite allows NULL in a
--     non-integer primary key otherwise.
--   * Lease expiry uses the application clock: there is a single server
--     process, so it is the same clock as the database.

-- +goose Up
CREATE TABLE users (
    id            TEXT    NOT NULL PRIMARY KEY,
    username      TEXT    NOT NULL,
    display_name  TEXT    NOT NULL DEFAULT '',
    email         TEXT,
    password_hash TEXT,
    is_admin      INTEGER NOT NULL DEFAULT 0,
    disabled_at   TEXT,
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
) STRICT;
-- lower() folds ASCII only in SQLite but all of Unicode in PostgreSQL;
-- usernames must be restricted to ASCII for the two to agree.
CREATE UNIQUE INDEX users_username_key ON users (lower(username));

CREATE TABLE access_tokens (
    id           TEXT NOT NULL PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users (id),
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL,
    scopes       TEXT NOT NULL,
    expires_at   TEXT,
    last_used_at TEXT,
    revoked_at   TEXT,
    created_at   TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX access_tokens_token_hash_key ON access_tokens (token_hash);
CREATE INDEX access_tokens_user_id_idx ON access_tokens (user_id);

CREATE TABLE projects (
    id            TEXT    NOT NULL PRIMARY KEY,
    name          TEXT    NOT NULL,
    owner_user_id TEXT    NOT NULL REFERENCES users (id),
    is_personal   INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
) STRICT;
CREATE INDEX projects_owner_user_id_idx ON projects (owner_user_id);
CREATE UNIQUE INDEX projects_personal_owner_key ON projects (owner_user_id) WHERE is_personal;

CREATE TABLE role_bindings (
    id         TEXT NOT NULL PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users (id),
    role       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX role_bindings_project_user_key ON role_bindings (project_id, user_id);
CREATE INDEX role_bindings_user_id_idx ON role_bindings (user_id);

CREATE TABLE audit_events (
    id            TEXT NOT NULL PRIMARY KEY,
    ts            TEXT NOT NULL,
    actor_kind    TEXT NOT NULL,
    actor_id      TEXT,
    action        TEXT NOT NULL,
    resource_kind TEXT NOT NULL,
    resource_id   TEXT,
    result        TEXT NOT NULL,
    ip            TEXT,
    details       TEXT
) STRICT;
CREATE INDEX audit_events_ts_idx ON audit_events (ts);

CREATE TABLE leases (
    id         TEXT NOT NULL PRIMARY KEY,
    name       TEXT NOT NULL,
    holder     TEXT NOT NULL,
    expires_at TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX leases_name_key ON leases (name);

-- +goose Down
DROP TABLE leases;
DROP TABLE audit_events;
DROP TABLE role_bindings;
DROP TABLE projects;
DROP TABLE access_tokens;
DROP TABLE users;
