-- OIDC identities, mobile pairing, deployment settings, profile
-- preferences, device sessions and richer audit rows for M4 (tech design
-- 12.1, 12.7). Keep in sync with sqlite/20261104012000_accounts.sql.

-- +goose Up
CREATE TABLE identities (
    id            uuid        PRIMARY KEY,
    user_id       uuid        NOT NULL REFERENCES users (id),
    issuer        text        NOT NULL,
    subject       text        NOT NULL,
    -- preferred_username and email as last seen from the provider.
    username      text,
    email         text,
    created_at    timestamptz NOT NULL,
    last_login_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX identities_issuer_subject_key ON identities (issuer, subject);
CREATE INDEX identities_user_id_idx ON identities (user_id);

-- One-time codes shown as a QR code in the web console (tech design 12.1).
CREATE TABLE pairing_codes (
    id         uuid        PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users (id),
    -- Hex SHA-256 of the code; the code itself is never stored.
    code_hash  text        NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    -- Set when exchanged, together with the device session it created.
    used_at    timestamptz,
    session_id uuid        REFERENCES sessions (id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX pairing_codes_code_hash_key ON pairing_codes (code_hash);
CREATE INDEX pairing_codes_user_id_idx ON pairing_codes (user_id);

-- Deployment settings changed in the admin console, such as
-- open_registration and local_password_login. value is JSON.
CREATE TABLE settings (
    id         uuid        PRIMARY KEY,
    key        text        NOT NULL,
    value      text        NOT NULL,
    updated_by uuid        REFERENCES users (id),
    updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX settings_key_key ON settings (key);

-- Profile preferences; NULL lets the client decide.
ALTER TABLE users ADD COLUMN locale text;
ALTER TABLE users ADD COLUMN timezone text;
-- Last request with any session or token of the user, written at most
-- once a minute like the per-credential timestamps.
ALTER TABLE users ADD COLUMN last_seen_at timestamptz;

-- Device sessions (kind mobile) describe the device they were paired from.
ALTER TABLE sessions ADD COLUMN device_name text;
ALTER TABLE sessions ADD COLUMN device_platform text;
ALTER TABLE sessions ADD COLUMN device_os_version text;

-- read, mutate or execute, from the operation's x-operation-kind.
ALTER TABLE audit_events ADD COLUMN kind text;
-- api, mcp, console, runner or hook; tool is the MCP tool name.
ALTER TABLE audit_events ADD COLUMN via text;
ALTER TABLE audit_events ADD COLUMN tool text;
-- Username, token name or runner name at the time of the event.
ALTER TABLE audit_events ADD COLUMN actor_name text;
ALTER TABLE audit_events ADD COLUMN request_id text;
CREATE INDEX audit_events_actor_ts_idx ON audit_events (actor_id, ts);
CREATE INDEX audit_events_resource_idx ON audit_events (resource_kind, resource_id);

-- +goose Down
DROP INDEX audit_events_resource_idx;
DROP INDEX audit_events_actor_ts_idx;
ALTER TABLE audit_events DROP COLUMN request_id;
ALTER TABLE audit_events DROP COLUMN actor_name;
ALTER TABLE audit_events DROP COLUMN tool;
ALTER TABLE audit_events DROP COLUMN via;
ALTER TABLE audit_events DROP COLUMN kind;
ALTER TABLE sessions DROP COLUMN device_os_version;
ALTER TABLE sessions DROP COLUMN device_platform;
ALTER TABLE sessions DROP COLUMN device_name;
ALTER TABLE users DROP COLUMN last_seen_at;
ALTER TABLE users DROP COLUMN timezone;
ALTER TABLE users DROP COLUMN locale;
DROP TABLE settings;
DROP TABLE pairing_codes;
DROP TABLE identities;
