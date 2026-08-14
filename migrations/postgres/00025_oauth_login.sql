-- migrations/postgres/00025_oauth_login.sql
-- Mirrors migrations/sqlite/00025_oauth_login.sql for the same reasons;
-- see that file for the full rationale.

-- +goose Up
CREATE TABLE oauth_providers (
    id                       BIGSERIAL PRIMARY KEY,
    slug                     VARCHAR(32) NOT NULL UNIQUE,
    name                     VARCHAR(64) NOT NULL,
    icon                     VARCHAR(255) NOT NULL DEFAULT '',
    enabled                  BOOLEAN NOT NULL DEFAULT FALSE,
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
    auth_style               VARCHAR(16) NOT NULL DEFAULT 'post',
    created_at               TIMESTAMPTZ NOT NULL,
    updated_at               TIMESTAMPTZ NOT NULL
);

CREATE TABLE user_identities (
    id                 BIGSERIAL PRIMARY KEY,
    user_id            BIGINT NOT NULL REFERENCES users(id),
    oauth_provider_id  BIGINT NOT NULL REFERENCES oauth_providers(id),
    provider_user_id   VARCHAR(255) NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL,
    UNIQUE(oauth_provider_id, provider_user_id)
);

CREATE INDEX idx_user_identities_user_id ON user_identities(user_id);

CREATE TABLE auth_states (
    id                 VARCHAR(64) PRIMARY KEY,
    oauth_provider_id  BIGINT NOT NULL REFERENCES oauth_providers(id),
    code_verifier      VARCHAR(128) NOT NULL,
    redirect_uri       VARCHAR(512) NOT NULL,
    consumed_at        TIMESTAMPTZ NULL,
    expires_at         TIMESTAMPTZ NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_auth_states_expires_at ON auth_states(expires_at);

-- +goose Down
DROP TABLE auth_states;
DROP TABLE user_identities;
DROP TABLE oauth_providers;
