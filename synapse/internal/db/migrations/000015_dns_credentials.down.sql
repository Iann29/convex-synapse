DROP INDEX IF EXISTS idx_deployment_domains_credential;
ALTER TABLE deployment_domains
    DROP COLUMN IF EXISTS dns_credential_id,
    DROP COLUMN IF EXISTS auto_configured;
DROP INDEX IF EXISTS idx_dns_credentials_provider;
DROP TABLE IF EXISTS dns_credentials;
