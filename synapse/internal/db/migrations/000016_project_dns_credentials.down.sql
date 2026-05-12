DROP INDEX IF EXISTS idx_dns_credentials_project;
ALTER TABLE dns_credentials DROP COLUMN IF EXISTS project_id;
