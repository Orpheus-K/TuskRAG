-- +goose Up
ALTER TABLE model_provider
  ADD COLUMN api_key_id text,
  ADD COLUMN api_key_nonce bytea,
  ADD COLUMN api_key_ciphertext bytea,
  ADD CONSTRAINT model_provider_credential_check CHECK (
    (api_key_id IS NULL AND api_key_nonce IS NULL AND api_key_ciphertext IS NULL)
    OR (api_key_id IS NOT NULL AND api_key_id <> '' AND api_key_nonce IS NOT NULL AND api_key_ciphertext IS NOT NULL
        AND octet_length(api_key_nonce) = 12 AND octet_length(api_key_ciphertext) BETWEEN 17 AND 4112)
  );
