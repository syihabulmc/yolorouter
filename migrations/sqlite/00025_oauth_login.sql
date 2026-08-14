-- migrations/sqlite/00025_oauth_login.sql
-- External login via any standard OAuth2/OIDC identity provider.
--
-- oauth_providers: admin-configured identity sources. One generic shape
-- covers every provider — three endpoints, credentials, scopes, and JSON
-- field paths for reading the userinfo response. client_secret is stored
-- as AES-GCM ciphertext under the same master key as provider keys.
--
-- user_identities: (provider, provider_user_id) -> user. The provider's
-- stable user id is the identity — never the username or email, both of
-- which can change or be reassigned upstream. One identity maps to one
-- account; there is no bind/merge flow by design.
--
-- auth_states: one-time login-flow credentials. id is the SHA-256 of a
-- 32-byte random state token (same recipe as user_sessions.id — the raw
-- token is high-entropy, so a leaked row is not replayable). Consumption
-- is a transactional conditional UPDATE on consumed_at: a state can be
-- spent exactly once, and only for the provider it was issued for.
-- code_verifier is the PKCE secret for this flow; redirect_uri pins the
-- exact value the authorization request used, so the token exchange
-- repeats it verbatim regardless of how the callback was reached.

-- +goose Up
CREATE TABLE oauth_providers (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    slug                     VARCHAR(32) NOT NULL UNIQUE,
    name                     VARCHAR(64) NOT NULL,
    icon                     VARCHAR(255) NOT NULL DEFAULT '',
    enabled                  BOOLEAN NOT NULL DEFAULT 0,
    client_id                VARCHAR(255) NOT NULL,
    encrypted_client_secret  TEXT NOT NULL,
    authorization_endpoint   VARCHAR(512) NOT NULL,
    token_endpoint           VARCHAR(512) NOT NULL,
    userinfo_endpoint        VARCHAR(512) NOT NULL,
    scopes                   VARCHAR(255) NOT NULL DEFAULT 'openid profile email',
    user_id_field            VARCHAR(128) NOT NULL DEFAULT 'sub',
    username_field           VARCHAR(128) NOT NULL DEFAULT 'preferred_username',
    display_name_field       VARCHAR(128) NOT NULL DEFAULT 'name',
    email_field              VARCHAR(128) NOT NULL DEFAULT 'email',
    -- 'basic' = client_secret_basic (Authorization header),
    -- 'post' = client_secret_post (form body).
    auth_style               VARCHAR(16) NOT NULL DEFAULT 'post',
    created_at               DATETIME NOT NULL,
    updated_at               DATETIME NOT NULL
);

CREATE TABLE user_identities (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id            INTEGER NOT NULL REFERENCES users(id),
    oauth_provider_id  INTEGER NOT NULL REFERENCES oauth_providers(id),
    provider_user_id   VARCHAR(255) NOT NULL,
    created_at         DATETIME NOT NULL,
    UNIQUE(oauth_provider_id, provider_user_id)
);

CREATE INDEX idx_user_identities_user_id ON user_identities(user_id);

CREATE TABLE auth_states (
    id                 VARCHAR(64) PRIMARY KEY,
    oauth_provider_id  INTEGER NOT NULL REFERENCES oauth_providers(id),
    code_verifier      VARCHAR(128) NOT NULL,
    redirect_uri       VARCHAR(512) NOT NULL,
    consumed_at        DATETIME NULL,
    expires_at         DATETIME NOT NULL,
    created_at         DATETIME NOT NULL
);

CREATE INDEX idx_auth_states_expires_at ON auth_states(expires_at);

-- +goose Down
DROP TABLE auth_states;
DROP TABLE user_identities;
DROP TABLE oauth_providers;
