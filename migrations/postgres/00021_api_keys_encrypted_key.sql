-- migrations/postgres/00021_api_keys_encrypted_key.sql
-- Mirrors migrations/sqlite/00021_api_keys_encrypted_key.sql for the
-- same reasons. AES-GCM ciphertext of the key plaintext for the list-page
-- reveal; NULL means the key predates the feature and cannot be recovered.
-- key_hash remains the auth-path index.

-- +goose Up
ALTER TABLE api_keys ADD COLUMN encrypted_key TEXT NULL;

-- +goose Down
ALTER TABLE api_keys DROP COLUMN IF EXISTS encrypted_key;
