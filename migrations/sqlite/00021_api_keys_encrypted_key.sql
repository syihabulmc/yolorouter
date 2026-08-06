-- migrations/sqlite/00021_api_keys_encrypted_key.sql
--
-- encrypted_key stores the AES-GCM ciphertext of the key plaintext, so the
-- full key can be revealed again from the API-key list page (copy button on
-- each row). NULL means the key predates this feature: its plaintext was never
-- persisted (only the SHA-256 key_hash survives), so it can never be recovered
-- and the reveal endpoint returns a dedicated error code. key_hash is kept as
-- the auth-path index (FindAPIKeyByHash); encrypted_key is read only on the
-- reveal path. Mirrors the provider-key encryption model (pkg/crypto AES-GCM).

-- +goose Up
ALTER TABLE api_keys ADD COLUMN encrypted_key TEXT NULL;

-- +goose Down
ALTER TABLE api_keys DROP COLUMN encrypted_key;
