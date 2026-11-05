-- Keyed fingerprints of credential values, for rejecting submissions that
-- contain them (tech design 11.7). Keep in sync with
-- postgres/20261107010000_credential_fingerprints.sql, which documents the
-- column.

-- +goose Up
ALTER TABLE credential_versions ADD COLUMN fingerprints TEXT;

-- +goose Down
ALTER TABLE credential_versions DROP COLUMN fingerprints;
