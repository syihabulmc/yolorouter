-- migrations/sqlite/00015_system_settings_and_custom_system_prompt.sql
--
-- SQLite mirror of migrations/postgres/00015_system_settings_and_custom_system_prompt.sql:
-- global system_settings key-value table + api_keys custom-system-prompt
-- override columns. SQLite uses INTEGER 0/1 for booleans and CURRENT_TIMESTAMP
-- instead of TIMESTAMPTZ/now().

-- +goose Up
CREATE TABLE system_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL DEFAULT '',
    version    INTEGER NOT NULL DEFAULT 1,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO system_settings (key, value) VALUES
    ('custom_system_prompt_enabled', 'false'),
    ('custom_system_prompt',         '');

ALTER TABLE api_keys ADD COLUMN custom_system_prompt_enabled          BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE api_keys ADD COLUMN custom_system_prompt_enabled_override BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE api_keys ADD COLUMN custom_system_prompt                  TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE api_keys DROP COLUMN custom_system_prompt;
ALTER TABLE api_keys DROP COLUMN custom_system_prompt_enabled_override;
ALTER TABLE api_keys DROP COLUMN custom_system_prompt_enabled;
DELETE FROM system_settings WHERE key IN ('custom_system_prompt_enabled','custom_system_prompt');
DROP TABLE system_settings;
