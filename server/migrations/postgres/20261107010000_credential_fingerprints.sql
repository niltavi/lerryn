-- Keyed fingerprints of credential values, for rejecting submissions that
-- contain them (tech design 11.7). Keep in sync with
-- sqlite/20261107010000_credential_fingerprints.sql.

-- +goose Up
-- JSON written by service.credentialFingerprints: the ID of the master key
-- the fingerprint keys were derived from, and for every sensitive value
-- and its common encodings an AES block of its first 8 bytes and an
-- HMAC-SHA256 of the whole form, both truncated and keyed from the master
-- key. Without the master key they reveal nothing about the values. NULL
-- for versions stored before this migration; the server fills it in the
-- first time it needs it.
ALTER TABLE credential_versions ADD COLUMN fingerprints text;

-- +goose Down
ALTER TABLE credential_versions DROP COLUMN fingerprints;
