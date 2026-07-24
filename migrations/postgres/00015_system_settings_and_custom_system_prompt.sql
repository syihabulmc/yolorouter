-- migrations/postgres/00015_system_settings_and_custom_system_prompt.sql
--
-- Global system_settings key-value table (with version for optimistic
-- lock) + api_keys custom-system-prompt override columns. The
-- system_settings rows seeded here are the single source of truth for
-- the custom-system-prompt global default; per-key columns below let an
-- individual key opt out of (or further customize) that default.

-- +goose Up
CREATE TABLE system_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL DEFAULT '',
    version    INTEGER NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO system_settings (key, value) VALUES
    ('custom_system_prompt_enabled', 'false'),
    ('custom_system_prompt',         '');

ALTER TABLE api_keys
    ADD COLUMN custom_system_prompt_enabled          BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN custom_system_prompt_enabled_override BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN custom_system_prompt                  VARCHAR(2000) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE api_keys DROP COLUMN IF EXISTS custom_system_prompt;
ALTER TABLE api_keys DROP COLUMN IF EXISTS custom_system_prompt_enabled_override;
ALTER TABLE api_keys DROP COLUMN IF EXISTS custom_system_prompt_enabled;
DELETE FROM system_settings WHERE key IN ('custom_system_prompt_enabled','custom_system_prompt');
DROP TABLE system_settings;
