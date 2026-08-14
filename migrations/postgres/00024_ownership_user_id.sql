-- migrations/postgres/00024_ownership_user_id.sql
-- Mirrors migrations/sqlite/00024_ownership_user_id.sql for the same
-- reasons; see that file for the full rationale (including why the
-- columns carry no REFERENCES clause).

-- +goose Up
ALTER TABLE api_keys ADD COLUMN user_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN user_id BIGINT NULL;

UPDATE api_keys SET user_id = COALESCE((SELECT id FROM users WHERE is_local), 0);
UPDATE request_logs SET user_id = (SELECT id FROM users WHERE is_local);

CREATE INDEX idx_api_keys_user_id ON api_keys (user_id);
CREATE INDEX idx_request_logs_user_id ON request_logs (user_id);

-- +goose Down
DROP INDEX IF EXISTS idx_request_logs_user_id;
DROP INDEX IF EXISTS idx_api_keys_user_id;
ALTER TABLE request_logs DROP COLUMN user_id;
ALTER TABLE api_keys DROP COLUMN user_id;
