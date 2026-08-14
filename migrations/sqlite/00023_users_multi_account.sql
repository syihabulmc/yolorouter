-- migrations/sqlite/00023_users_multi_account.sql
-- Replace the single-admin tables (admins / admin_sessions) with a
-- multi-account users table + user_sessions. The existing admin row is
-- carried over as the first user with role 'admin'; it is also flagged
-- is_local = 1, marking it as the one password-login account (future
-- accounts authenticate through external identity providers and have an
-- empty password_hash).
--
-- The old admins.singleton_guard UNIQUE column enforced "exactly one
-- admin" at the database level. Multi-account keeps a weaker but still
-- database-enforced invariant: at most one *local* (password) account,
-- via a partial unique index on is_local = 1 — concurrent first-run
-- setup requests race on that index instead of an app-level
-- check-then-act. Any number of admins is now allowed.
--
-- NOTE: this migration drops the old tables, so it is not
-- rolling-upgrade safe — binaries older than this schema cannot run
-- against the migrated database. Restart all instances on the new
-- binary when upgrading.

-- +goose Up
CREATE TABLE users (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    username            VARCHAR(32) NOT NULL UNIQUE,
    -- Empty string (not NULL) for accounts without password login: GORM
    -- inserts every mapped column anyway, and '' fails bcrypt comparison
    -- safely, so there is no separate "has no password" NULL state to
    -- mishandle.
    password_hash       VARCHAR(255) NOT NULL DEFAULT '',
    display_name        VARCHAR(128) NOT NULL DEFAULT '',
    email               VARCHAR(255) NOT NULL DEFAULT '',
    role                VARCHAR(16) NOT NULL DEFAULT 'member',
    -- 1 = enabled, 2 = disabled. Deliberately avoids 0 (the Go zero
    -- value) so an accidentally unset status can never read as valid —
    -- same convention as api_keys.status.
    status              INTEGER NOT NULL DEFAULT 1,
    is_local            BOOLEAN NOT NULL DEFAULT 0,
    failed_login_count  INTEGER NOT NULL DEFAULT 0,
    locked_until        DATETIME NULL,
    last_login_at       DATETIME NULL,
    created_at          DATETIME NOT NULL,
    updated_at          DATETIME NOT NULL
);

CREATE UNIQUE INDEX idx_users_single_local ON users(is_local) WHERE is_local = 1;

INSERT INTO users (id, username, password_hash, role, status, is_local,
                   failed_login_count, locked_until, created_at, updated_at)
SELECT id, username, password_hash, 'admin', 1, 1,
       failed_login_count, locked_until, created_at, updated_at
FROM admins;

CREATE TABLE user_sessions (
    id          VARCHAR(64) PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    expires_at  DATETIME NOT NULL,
    created_at  DATETIME NOT NULL
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
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    username            VARCHAR(32) NOT NULL UNIQUE,
    password_hash       VARCHAR(255) NOT NULL,
    failed_login_count  INTEGER NOT NULL DEFAULT 0,
    locked_until        DATETIME NULL,
    singleton_guard     SMALLINT NOT NULL DEFAULT 1 UNIQUE,
    created_at          DATETIME NOT NULL,
    updated_at          DATETIME NOT NULL
);

INSERT INTO admins (id, username, password_hash, failed_login_count, locked_until, created_at, updated_at)
SELECT id, username, password_hash, failed_login_count, locked_until, created_at, updated_at
FROM users WHERE is_local = 1;

CREATE TABLE admin_sessions (
    id          VARCHAR(64) PRIMARY KEY,
    admin_id    INTEGER NOT NULL REFERENCES admins(id),
    expires_at  DATETIME NOT NULL,
    created_at  DATETIME NOT NULL
);

CREATE INDEX idx_admin_sessions_admin_id ON admin_sessions(admin_id);
CREATE INDEX IF NOT EXISTS idx_admin_sessions_expires_at ON admin_sessions(expires_at);

INSERT INTO admin_sessions (id, admin_id, expires_at, created_at)
SELECT s.id, s.user_id, s.expires_at, s.created_at
FROM user_sessions s JOIN users u ON u.id = s.user_id
WHERE u.is_local = 1;

DROP TABLE user_sessions;
DROP TABLE users;
