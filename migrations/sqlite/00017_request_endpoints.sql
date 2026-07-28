-- migrations/sqlite/00017_request_endpoints.sql
--
-- SQLite mirror of migrations/postgres/00017_request_endpoints.sql: adds the
-- caller-facing request_path and the dispatched upstream_url to request_logs.
-- Both columns are plain TEXT with a '' default on both dialects, so this
-- migration has no SQLite-specific type differences from the postgres version.

-- +goose Up
ALTER TABLE request_logs ADD COLUMN request_path TEXT NOT NULL DEFAULT '';
ALTER TABLE request_logs ADD COLUMN upstream_url TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE request_logs DROP COLUMN upstream_url;
ALTER TABLE request_logs DROP COLUMN request_path;
