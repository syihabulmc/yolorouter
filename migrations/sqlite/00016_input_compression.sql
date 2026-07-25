-- migrations/sqlite/00016_input_compression.sql
--
-- SQLite mirror of migrations/postgres/00016_input_compression.sql:
-- input-compression global switch (one system_settings row), per-key
-- override columns on api_keys, per-request savings columns on
-- request_logs, and a debug column on request_log_bodies for the
-- post-compression body. SQLite uses INTEGER 0/1 for booleans and
-- CURRENT_TIMESTAMP instead of TIMESTAMPTZ/now().

-- +goose Up
INSERT INTO system_settings (key, value) VALUES ('input_compression_enabled', 'false');

ALTER TABLE api_keys ADD COLUMN compress_enabled          BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE api_keys ADD COLUMN compress_enabled_override BOOLEAN NOT NULL DEFAULT 0;

ALTER TABLE request_logs ADD COLUMN compress_estimated_tokens_saved      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN compress_estimated_cost_saved_micros INTEGER NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN compress_skip_reason                 TEXT    NOT NULL DEFAULT '';
ALTER TABLE request_logs ADD COLUMN compressors_applied                  TEXT    NOT NULL DEFAULT '';

ALTER TABLE request_log_bodies ADD COLUMN compressed_request_body TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE request_log_bodies DROP COLUMN compressed_request_body;
ALTER TABLE request_logs DROP COLUMN compressors_applied;
ALTER TABLE request_logs DROP COLUMN compress_skip_reason;
ALTER TABLE request_logs DROP COLUMN compress_estimated_cost_saved_micros;
ALTER TABLE request_logs DROP COLUMN compress_estimated_tokens_saved;
ALTER TABLE api_keys DROP COLUMN compress_enabled_override;
ALTER TABLE api_keys DROP COLUMN compress_enabled;
DELETE FROM system_settings WHERE key = 'input_compression_enabled';
