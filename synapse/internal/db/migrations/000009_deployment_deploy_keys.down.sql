DROP INDEX IF EXISTS deploy_keys_active_name;

ALTER TABLE deploy_keys
    DROP COLUMN IF EXISTS revoked_at,
    DROP COLUMN IF EXISTS admin_key_prefix;

ALTER TABLE deploy_keys
    RENAME COLUMN admin_key_hash TO token_hash;

ALTER TABLE deploy_keys
    ADD CONSTRAINT deploy_keys_token_hash_key UNIQUE (token_hash);
