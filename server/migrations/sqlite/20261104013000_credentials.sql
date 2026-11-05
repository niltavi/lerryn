-- Credentials, their encrypted versions, grants to tasks and fill requests
-- for M4 (tech design 12.3 to 12.5). Keep in sync with
-- postgres/20261104013000_credentials.sql, which documents the columns.

-- +goose Up
CREATE TABLE credentials (
    id                 TEXT    NOT NULL PRIMARY KEY,
    owner_user_id      TEXT    NOT NULL REFERENCES users (id),
    name               TEXT    NOT NULL,
    purpose            TEXT    NOT NULL,
    fields             TEXT    NOT NULL,
    status             TEXT    NOT NULL DEFAULT 'active',
    current_version    INTEGER NOT NULL,
    expires_at         TEXT,
    revoked_at         TEXT,
    last_used_at       TEXT,
    expiry_notified_at TEXT,
    created_at         TEXT    NOT NULL,
    updated_at         TEXT    NOT NULL
) STRICT;
CREATE UNIQUE INDEX credentials_owner_name_key ON credentials (owner_user_id, name) WHERE revoked_at IS NULL;

CREATE TABLE credential_versions (
    id            TEXT    NOT NULL PRIMARY KEY,
    credential_id TEXT    NOT NULL REFERENCES credentials (id) ON DELETE CASCADE,
    version       INTEGER NOT NULL,
    ciphertext    BLOB    NOT NULL,
    wrapped_dek   BLOB    NOT NULL,
    kek_id        TEXT    NOT NULL,
    created_by    TEXT    NOT NULL REFERENCES users (id),
    created_at    TEXT    NOT NULL
) STRICT;
CREATE UNIQUE INDEX credential_versions_credential_version_key ON credential_versions (credential_id, version);

CREATE TABLE credential_grants (
    id            TEXT NOT NULL PRIMARY KEY,
    credential_id TEXT NOT NULL REFERENCES credentials (id) ON DELETE CASCADE,
    task_id       TEXT NOT NULL REFERENCES tasks (id),
    name          TEXT NOT NULL,
    granted_by    TEXT NOT NULL REFERENCES users (id),
    created_at    TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX credential_grants_credential_task_key ON credential_grants (credential_id, task_id);
CREATE UNIQUE INDEX credential_grants_task_name_key ON credential_grants (task_id, name);

CREATE TABLE credential_requests (
    id            TEXT NOT NULL PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users (id),
    task_id       TEXT REFERENCES tasks (id),
    credential_id TEXT REFERENCES credentials (id),
    name          TEXT NOT NULL,
    purpose       TEXT NOT NULL,
    fields        TEXT NOT NULL,
    status        TEXT NOT NULL,
    client        TEXT,
    created_at    TEXT NOT NULL,
    expires_at    TEXT NOT NULL,
    fulfilled_at  TEXT,
    cancelled_at  TEXT
) STRICT;
CREATE INDEX credential_requests_user_status_idx ON credential_requests (user_id, status, created_at);
CREATE INDEX credential_requests_task_idx ON credential_requests (task_id, name) WHERE task_id IS NOT NULL;

-- +goose Down
DROP TABLE credential_requests;
DROP TABLE credential_grants;
DROP TABLE credential_versions;
DROP TABLE credentials;
