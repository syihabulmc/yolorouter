-- migrations/sqlite/00012_provider_protocol_endpoints.sql
--
-- protocol_endpoints holds a JSON object mapping extra supported protocols
-- (beyond the provider's primary provider_type) to an optional per-protocol
-- base URL override. An empty string means "no extra protocols" — the
-- provider behaves exactly as before, using only base_url for
-- provider_type's protocol.

-- +goose Up
ALTER TABLE providers ADD COLUMN protocol_endpoints TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE providers DROP COLUMN protocol_endpoints;
