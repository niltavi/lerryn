-- Credentials, their encrypted versions, grants to tasks and fill requests
-- for M4 (tech design 12.3 to 12.5). Keep in sync with
-- sqlite/20261104013000_credentials.sql.
--
-- No column here ever holds plaintext. Values are encrypted per version
-- with their own data key; the data key is wrapped by the master key,
-- which lives outside the database.

-- +goose Up
CREATE TABLE credentials (
    id              uuid        PRIMARY KEY,
    owner_user_id   uuid        NOT NULL REFERENCES users (id),
    -- The reference name, also the default environment variable name.
    name            text        NOT NULL,
    purpose         text        NOT NULL,
    -- JSON array of the field specs (name, label, secret, ...), no values.
    fields          text        NOT NULL,
    -- active, revoked or expired.
    status          text        NOT NULL DEFAULT 'active',
    current_version integer     NOT NULL,
    expires_at      timestamptz,
    revoked_at      timestamptz,
    last_used_at    timestamptz,
    -- When the credential_expiring notification went out, so it goes once.
    expiry_notified_at timestamptz,
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL
);
-- A user has one live credential per name; revoked ones keep theirs.
CREATE UNIQUE INDEX credentials_owner_name_key ON credentials (owner_user_id, name) WHERE revoked_at IS NULL;

CREATE TABLE credential_versions (
    id            uuid        PRIMARY KEY,
    credential_id uuid        NOT NULL REFERENCES credentials (id) ON DELETE CASCADE,
    version       integer     NOT NULL,
    -- AES-256-GCM over the JSON object of field values, nonce prepended;
    -- the additional data binds the credential ID and version.
    ciphertext    bytea       NOT NULL,
    -- The data key, encrypted by the master key kek_id.
    wrapped_dek   bytea       NOT NULL,
    kek_id        text        NOT NULL,
    created_by    uuid        NOT NULL REFERENCES users (id),
    created_at    timestamptz NOT NULL
);
CREATE UNIQUE INDEX credential_versions_credential_version_key ON credential_versions (credential_id, version);

-- The owner lets a task use a credential under the manifest reference
-- name. One credential per (task, name).
CREATE TABLE credential_grants (
    id            uuid        PRIMARY KEY,
    credential_id uuid        NOT NULL REFERENCES credentials (id) ON DELETE CASCADE,
    task_id       uuid        NOT NULL REFERENCES tasks (id),
    name          text        NOT NULL,
    granted_by    uuid        NOT NULL REFERENCES users (id),
    created_at    timestamptz NOT NULL
);
CREATE UNIQUE INDEX credential_grants_credential_task_key ON credential_grants (credential_id, task_id);
CREATE UNIQUE INDEX credential_grants_task_name_key ON credential_grants (task_id, name);

CREATE TABLE credential_requests (
    id            uuid        PRIMARY KEY,
    -- The user who must fill it in: the caller that created the request.
    user_id       uuid        NOT NULL REFERENCES users (id),
    task_id       uuid        REFERENCES tasks (id),
    -- The credential being updated, or the one created on fulfilment.
    credential_id uuid        REFERENCES credentials (id),
    name          text        NOT NULL,
    purpose       text        NOT NULL,
    -- JSON array of the requested field specs.
    fields        text        NOT NULL,
    -- pending, fulfilled, expired or cancelled.
    status        text        NOT NULL,
    -- Name of the access token the request was created with.
    client        text,
    created_at    timestamptz NOT NULL,
    expires_at    timestamptz NOT NULL,
    fulfilled_at  timestamptz,
    cancelled_at  timestamptz
);
CREATE INDEX credential_requests_user_status_idx ON credential_requests (user_id, status, created_at);
CREATE INDEX credential_requests_task_idx ON credential_requests (task_id, name) WHERE task_id IS NOT NULL;

-- +goose Down
DROP TABLE credential_requests;
DROP TABLE credential_grants;
DROP TABLE credential_versions;
DROP TABLE credentials;
