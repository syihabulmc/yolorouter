-- migrations/sqlite/00024_ownership_user_id.sql
-- Attach ownership to API keys and request logs.
--
-- api_keys.user_id: every key belongs to exactly one account. NOT NULL
-- with DEFAULT 0 rather than a nullable column: a key without an owner is
-- not a valid state going forward, and 0 can never match a users.id, so a
-- row that somehow skipped the application-side owner assignment matches
-- no one's view instead of leaking into someone else's. Existing keys are
-- backfilled to the local (password) account — the only account that
-- could have created them under the single-admin model.
--
-- request_logs.user_id: nullable, mirroring api_key_id — rows written for
-- unauthenticated requests (the auth-failure audit path) have no owner,
-- exactly like their api_key_id is NULL. Historical rows are backfilled
-- to the local account wholesale: they were all produced under the
-- single-admin model, so the admin's per-user view of history equals the
-- pre-upgrade global view.
--
-- Neither column carries a REFERENCES clause: SQLite's ALTER TABLE ADD
-- COLUMN cannot add a foreign key with a non-NULL default, and the
-- Postgres tree stays symmetric with this one. Referential integrity is
-- owned by the write paths (and pinned by tests) instead.

-- +goose Up
ALTER TABLE api_keys ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN user_id INTEGER NULL;

UPDATE api_keys SET user_id = COALESCE((SELECT id FROM users WHERE is_local = 1), 0);
UPDATE request_logs SET user_id = (SELECT id FROM users WHERE is_local = 1);

CREATE INDEX idx_api_keys_user_id ON api_keys (user_id);
CREATE INDEX idx_request_logs_user_id ON request_logs (user_id);

-- +goose Down
DROP INDEX IF EXISTS idx_request_logs_user_id;
DROP INDEX IF EXISTS idx_api_keys_user_id;
ALTER TABLE request_logs DROP COLUMN user_id;
ALTER TABLE api_keys DROP COLUMN user_id;
