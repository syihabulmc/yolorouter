-- migrations/postgres/00017_request_endpoints.sql
--
-- Capture the caller-facing request path and the upstream URL the gateway
-- actually dispatched to, so the request-log detail page can surface both
-- endpoints alongside the existing audit fields. request_path is the path
-- the client hit (e.g. /v1/chat/completions); upstream_url is the full URL
-- the gateway sent to the provider for the final attempt. Both are optional
-- text: rows written before this migration backfill to '' and the UI renders
-- the em-dash placeholder for empty values.

-- +goose Up
ALTER TABLE request_logs ADD COLUMN request_path TEXT NOT NULL DEFAULT '';
ALTER TABLE request_logs ADD COLUMN upstream_url TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE request_logs DROP COLUMN IF EXISTS upstream_url;
ALTER TABLE request_logs DROP COLUMN IF EXISTS request_path;
