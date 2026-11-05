-- OIDC identities, mobile pairing, deployment settings, profile
-- preferences, device sessions and richer audit rows for M4 (tech design
-- 12.1, 12.7). Keep in sync with postgres/20261104012000_accounts.sql,
-- which documents the columns.

-- +goose Up
CREATE TABLE identities (
    id            TEXT NOT NULL PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users (id),
    issuer        TEXT NOT NULL,
    subject       TEXT NOT NULL,
    username      TEXT,
    email         TEXT,
    created_at    TEXT NOT NULL,
    last_login_at TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX identities_issuer_subject_key ON identities (issuer, subject);
CREATE INDEX identities_user_id_idx ON identities (user_id);

CREATE TABLE pairing_codes (
    id         TEXT NOT NULL PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users (id),
    code_hash  TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at    TEXT,
    session_id TEXT REFERENCES sessions (id) ON DELETE SET NULL
) STRICT;
CREATE UNIQUE INDEX pairing_codes_code_hash_key ON pairing_codes (code_hash);
CREATE INDEX pairing_codes_user_id_idx ON pairing_codes (user_id);

CREATE TABLE settings (
    id         TEXT NOT NULL PRIMARY KEY,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    updated_by TEXT REFERENCES users (id),
    updated_at TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX settings_key_key ON settings (key);

ALTER TABLE users ADD COLUMN locale TEXT;
ALTER TABLE users ADD COLUMN timezone TEXT;
ALTER TABLE users ADD COLUMN last_seen_at TEXT;

ALTER TABLE sessions ADD COLUMN device_name TEXT;
ALTER TABLE sessions ADD COLUMN device_platform TEXT;
ALTER TABLE sessions ADD COLUMN device_os_version TEXT;

ALTER TABLE audit_events ADD COLUMN kind TEXT;
ALTER TABLE audit_events ADD COLUMN via TEXT;
ALTER TABLE audit_events ADD COLUMN tool TEXT;
ALTER TABLE audit_events ADD COLUMN actor_name TEXT;
ALTER TABLE audit_events ADD COLUMN request_id TEXT;
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
