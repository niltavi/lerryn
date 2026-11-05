-- Initial schema: accounts, projects, audit and leases (tech design 5.5).
-- Keep in sync with sqlite/20261102120000_initial_schema.sql.
--
-- Conventions (tech design 6.3): primary keys are application-generated
-- UUIDv7; the application writes every timestamp in UTC, except lease
-- expiry, which uses the database clock; JSON columns are stored as text
-- and never queried; enum values are plain text checked in Go, so adding
-- a value never needs a migration.

-- +goose Up
CREATE TABLE users (
    id            uuid        PRIMARY KEY,
    username      text        NOT NULL,
    display_name  text        NOT NULL DEFAULT '',
    email         text,
    -- Empty for OIDC-only users.
    password_hash text,
    is_admin      boolean     NOT NULL DEFAULT false,
    disabled_at   timestamptz,
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL
);
-- Usernames are unique regardless of case. Lookups must use
-- lower(username) = lower(?) to hit this index.
CREATE UNIQUE INDEX users_username_key ON users (lower(username));

CREATE TABLE access_tokens (
    id           uuid        PRIMARY KEY,
    user_id      uuid        NOT NULL REFERENCES users (id),
    name         text        NOT NULL,
    -- Hex SHA-256 of the token; the token itself is never stored.
    token_hash   text        NOT NULL,
    -- JSON array of scopes: read, write, execute.
    scopes       text        NOT NULL,
    expires_at   timestamptz,
    last_used_at timestamptz,
    revoked_at   timestamptz,
    created_at   timestamptz NOT NULL
);
CREATE UNIQUE INDEX access_tokens_token_hash_key ON access_tokens (token_hash);
CREATE INDEX access_tokens_user_id_idx ON access_tokens (user_id);

CREATE TABLE projects (
    id            uuid        PRIMARY KEY,
    name          text        NOT NULL,
    owner_user_id uuid        NOT NULL REFERENCES users (id),
    is_personal   boolean     NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL
);
CREATE INDEX projects_owner_user_id_idx ON projects (owner_user_id);
-- Every user has exactly one personal project (tech design 12.2).
CREATE UNIQUE INDEX projects_personal_owner_key ON projects (owner_user_id) WHERE is_personal;

CREATE TABLE role_bindings (
    id         uuid        PRIMARY KEY,
    project_id uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users (id),
    -- viewer, maintainer or owner.
    role       text        NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX role_bindings_project_user_key ON role_bindings (project_id, user_id);
CREATE INDEX role_bindings_user_id_idx ON role_bindings (user_id);

-- Audit rows outlive the resources they mention, so nothing here is a
-- foreign key.
CREATE TABLE audit_events (
    id            uuid        PRIMARY KEY,
    ts            timestamptz NOT NULL,
    -- user, token or runner.
    actor_kind    text        NOT NULL,
    actor_id      uuid,
    action        text        NOT NULL,
    resource_kind text        NOT NULL,
    -- Text rather than uuid: some resources, such as settings, are keyed by name.
    resource_id   text,
    result        text        NOT NULL,
    ip            text,
    -- JSON object, never contains secrets.
    details       text
);
CREATE INDEX audit_events_ts_idx ON audit_events (ts);

-- Named leases, for example the leader lease (tech design 4.4).
-- expires_at is written with the database clock.
CREATE TABLE leases (
    id         uuid        PRIMARY KEY,
    name       text        NOT NULL,
    holder     text        NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX leases_name_key ON leases (name);

-- +goose Down
DROP TABLE leases;
DROP TABLE audit_events;
DROP TABLE role_bindings;
DROP TABLE projects;
DROP TABLE access_tokens;
DROP TABLE users;
