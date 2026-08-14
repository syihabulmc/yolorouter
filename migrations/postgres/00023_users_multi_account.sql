-- migrations/postgres/00023_users_multi_account.sql
-- Mirrors migrations/sqlite/00023_users_multi_account.sql for the same
-- reasons; see that file for the full rationale. Postgres-specific
-- additions: sequences must be advanced past the explicitly-copied ids
-- (an INSERT with an explicit id does not touch the BIGSERIAL sequence,
-- so the next generated id would collide), hence the setval calls.
--
-- NOTE: this migration drops the old tables, so it is not
-- rolling-upgrade safe — binaries older than this schema cannot run
-- against the migrated database. Restart all instances on the new
-- binary when upgrading.

-- +goose Up
CREATE TABLE users (
    id                  BIGSERIAL PRIMARY KEY,
    username            VARCHAR(32) NOT NULL UNIQUE,
    password_hash       VARCHAR(255) NOT NULL DEFAULT '',
    display_name        VARCHAR(128) NOT NULL DEFAULT '',
    email               VARCHAR(255) NOT NULL DEFAULT '',
    role                VARCHAR(16) NOT NULL DEFAULT 'member',
    -- 1 = enabled, 2 = disabled. Deliberately avoids 0 (the Go zero
    -- value) so an accidentally unset status can never read as valid —
    -- same convention as api_keys.status.
    status              INTEGER NOT NULL DEFAULT 1,
    is_local            BOOLEAN NOT NULL DEFAULT FALSE,
    failed_login_count  INTEGER NOT NULL DEFAULT 0,
    locked_until        TIMESTAMPTZ NULL,
    last_login_at       TIMESTAMPTZ NULL,
    created_at          TIMESTAMPTZ NOT NULL,
    updated_at          TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX idx_users_single_local ON users(is_local) WHERE is_local;

INSERT INTO users (id, username, password_hash, role, status, is_local,
                   failed_login_count, locked_until, created_at, updated_at)
SELECT id, username, password_hash, 'admin', 1, TRUE,
       failed_login_count, locked_until, created_at, updated_at
FROM admins;

SELECT setval(pg_get_serial_sequence('users', 'id'),
              COALESCE((SELECT MAX(id) FROM users), 0) + 1, false);

CREATE TABLE user_sessions (
    id          VARCHAR(64) PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id),
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_user_sessions_user_id ON user_sessions(user_id);
CREATE INDEX idx_user_sessions_expires_at ON user_sessions(expires_at);

-- Session ids are preserved so already-issued session cookies stay valid
-- across the upgrade.
INSERT INTO user_sessions (id, user_id, expires_at, created_at)
SELECT id, admin_id, expires_at, created_at FROM admin_sessions;

DROP TABLE admin_sessions;
DROP TABLE admins;

-- +goose Down
-- The single-admin schema can only represent the local password account;
-- non-local users and their sessions are dropped by this rollback.
CREATE TABLE admins (
    id                  BIGSERIAL PRIMARY KEY,
    username            VARCHAR(32) NOT NULL UNIQUE,
    password_hash       VARCHAR(255) NOT NULL,
    failed_login_count  INTEGER NOT NULL DEFAULT 0,
    locked_until        TIMESTAMPTZ NULL,
    singleton_guard     SMALLINT NOT NULL DEFAULT 1 UNIQUE,
    created_at          TIMESTAMPTZ NOT NULL,
    updated_at          TIMESTAMPTZ NOT NULL
);

INSERT INTO admins (id, username, password_hash, failed_login_count, locked_until, created_at, updated_at)
SELECT id, username, password_hash, failed_login_count, locked_until, created_at, updated_at
FROM users WHERE is_local;

SELECT setval(pg_get_serial_sequence('admins', 'id'),
              COALESCE((SELECT MAX(id) FROM admins), 0) + 1, false);

CREATE TABLE admin_sessions (
    id          VARCHAR(64) PRIMARY KEY,
    admin_id    BIGINT NOT NULL REFERENCES admins(id),
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_admin_sessions_admin_id ON admin_sessions(admin_id);
CREATE INDEX IF NOT EXISTS idx_admin_sessions_expires_at ON admin_sessions(expires_at);

INSERT INTO admin_sessions (id, admin_id, expires_at, created_at)
SELECT s.id, s.user_id, s.expires_at, s.created_at
FROM user_sessions s JOIN users u ON u.id = s.user_id
WHERE u.is_local;

DROP TABLE user_sessions;
DROP TABLE users;
