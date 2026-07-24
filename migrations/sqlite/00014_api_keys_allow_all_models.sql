-- migrations/sqlite/00014_api_keys_allow_all_models.sql
--
-- allow_all_models lets a key bypass the per-model allowlist entirely: when
-- true the gateway permits any enabled model and api_key_models is ignored.
-- Defaults to FALSE so pre-existing keys keep their explicit allowlist (an
-- empty allowlist still means "no models") — only keys created after this
-- column exists opt into all-models access.

-- +goose Up
ALTER TABLE api_keys ADD COLUMN allow_all_models BOOLEAN NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE api_keys DROP COLUMN allow_all_models;
