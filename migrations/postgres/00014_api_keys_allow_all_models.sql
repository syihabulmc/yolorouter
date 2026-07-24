-- migrations/postgres/00014_api_keys_allow_all_models.sql
-- Mirrors migrations/sqlite/00014_api_keys_allow_all_models.sql for the
-- same reasons.

-- +goose Up
ALTER TABLE api_keys ADD COLUMN allow_all_models BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE api_keys DROP COLUMN IF EXISTS allow_all_models;
