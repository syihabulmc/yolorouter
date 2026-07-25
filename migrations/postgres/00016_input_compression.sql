-- migrations/postgres/00016_input_compression.sql
--
-- Input compression: a single system_settings seed row acts as the
-- global on/off switch, per-key override columns on api_keys allow
-- individual keys to opt in or out, per-request savings columns on
-- request_logs record the cost/pacing impact, and request_log_bodies
-- gains a debug column capturing the post-compression body.

-- +goose Up
INSERT INTO system_settings (key, value) VALUES ('input_compression_enabled', 'false');

ALTER TABLE api_keys
    ADD COLUMN compress_enabled          BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN compress_enabled_override BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE request_logs
    ADD COLUMN compress_estimated_tokens_saved      INT          NOT NULL DEFAULT 0,
    ADD COLUMN compress_estimated_cost_saved_micros BIGINT       NOT NULL DEFAULT 0,
    ADD COLUMN compress_skip_reason                 VARCHAR(32)  NOT NULL DEFAULT '',
    ADD COLUMN compressors_applied                  TEXT         NOT NULL DEFAULT '';

ALTER TABLE request_log_bodies
    ADD COLUMN compressed_request_body TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE request_log_bodies DROP COLUMN IF EXISTS compressed_request_body;
ALTER TABLE request_logs DROP COLUMN IF EXISTS compressors_applied;
ALTER TABLE request_logs DROP COLUMN IF EXISTS compress_skip_reason;
ALTER TABLE request_logs DROP COLUMN IF EXISTS compress_estimated_cost_saved_micros;
ALTER TABLE request_logs DROP COLUMN IF EXISTS compress_estimated_tokens_saved;
ALTER TABLE api_keys DROP COLUMN IF EXISTS compress_enabled_override;
ALTER TABLE api_keys DROP COLUMN IF EXISTS compress_enabled;
DELETE FROM system_settings WHERE key = 'input_compression_enabled';
