-- migrations/postgres/00012_provider_protocol_endpoints.sql
-- Mirrors migrations/sqlite/00012_provider_protocol_endpoints.sql for the
-- same reasons.

-- +goose Up
ALTER TABLE providers ADD COLUMN protocol_endpoints TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE providers DROP COLUMN IF EXISTS protocol_endpoints;
