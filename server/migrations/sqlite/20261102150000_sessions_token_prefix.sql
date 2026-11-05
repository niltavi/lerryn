-- Login sessions for the web console and the mobile app (tech design 12.1),
-- and the display prefix of access tokens.
-- Keep in sync with postgres/20261102150000_sessions_token_prefix.sql.

-- +goose Up
CREATE TABLE sessions (
    id           TEXT NOT NULL PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users (id),
    token_hash   TEXT NOT NULL,
    kind         TEXT NOT NULL,
    ip           TEXT,
    user_agent   TEXT,
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX sessions_token_hash_key ON sessions (token_hash);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);

-- The first characters of the token, shown so users can tell tokens apart.
ALTER TABLE access_tokens ADD COLUMN prefix TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE access_tokens DROP COLUMN prefix;
DROP TABLE sessions;
