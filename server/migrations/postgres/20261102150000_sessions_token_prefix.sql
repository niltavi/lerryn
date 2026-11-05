-- Login sessions for the web console and the mobile app (tech design 12.1),
-- and the display prefix of access tokens.
-- Keep in sync with sqlite/20261102150000_sessions_token_prefix.sql.

-- +goose Up
CREATE TABLE sessions (
    id           uuid        PRIMARY KEY,
    user_id      uuid        NOT NULL REFERENCES users (id),
    -- Hex SHA-256 of the session token; the token itself is never stored.
    token_hash   text        NOT NULL,
    -- web or mobile.
    kind         text        NOT NULL,
    ip           text,
    user_agent   text,
    created_at   timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX sessions_token_hash_key ON sessions (token_hash);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);

-- The first characters of the token, shown so users can tell tokens apart.
ALTER TABLE access_tokens ADD COLUMN prefix text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE access_tokens DROP COLUMN prefix;
DROP TABLE sessions;
