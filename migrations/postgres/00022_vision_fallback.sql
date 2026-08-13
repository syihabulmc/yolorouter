-- migrations/postgres/00022_vision_fallback.sql
--
-- Vision fallback: when a caller sends images to a model declared unable to
-- read them, the gateway describes the images through a designated vision
-- model and forwards text instead.
--
-- models.supports_image_input is a tri-state declaration filled by the admin:
-- NULL means undeclared — the gateway must not intervene (neither describe
-- nor strip), so missing metadata can never misjudge a vision-capable model.
--
-- request_logs.source distinguishes gateway-initiated sub-calls ('' = a
-- normal caller request, 'vision_fallback' = an image-description sub-call);
-- parent_request_id links a sub-call to the caller request that spawned it.
-- Sub-calls are separate log rows so each row's tokens describe exactly one
-- model; the description cost appears in the key's totals through the
-- sub-call's own row, not inside the parent request's row.

-- The two settings rows share one version (CAS on save, same contract as the
-- custom-system-prompt pair). An empty model name means the feature is off;
-- an empty prompt means "use the built-in default prompt".

-- +goose Up
ALTER TABLE models ADD COLUMN supports_image_input BOOLEAN NULL;
ALTER TABLE request_logs ADD COLUMN source TEXT NOT NULL DEFAULT '';
ALTER TABLE request_logs ADD COLUMN parent_request_id TEXT NOT NULL DEFAULT '';

INSERT INTO system_settings (key, value) VALUES
    ('vision_fallback_model',  ''),
    ('vision_fallback_prompt', '');

-- +goose Down
DELETE FROM system_settings WHERE key IN ('vision_fallback_model', 'vision_fallback_prompt');
ALTER TABLE models DROP COLUMN IF EXISTS supports_image_input;
ALTER TABLE request_logs DROP COLUMN IF EXISTS source;
ALTER TABLE request_logs DROP COLUMN IF EXISTS parent_request_id;
