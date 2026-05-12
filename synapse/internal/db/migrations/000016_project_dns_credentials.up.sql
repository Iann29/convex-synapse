-- v1.6.4+ — Scope DNS credentials to a project. Existing rows stay
-- NULL on project_id and behave as instance-wide credentials (gated
-- by /v1/admin/dns_credentials). New rows created via the project
-- endpoint carry project_id, are visible to project admins only,
-- and win the lookup priority over global credentials covering the
-- same zone.
--
-- Rationale: an agency operator running Synapse for N clients wants
-- each client's Cloudflare token to live alongside the client's
-- project, not pooled in a single admin panel. The hierarchy keeps
-- backward compat: a single-operator install with one global token
-- (the v1.5 default) sees zero behavior change.

ALTER TABLE dns_credentials
    ADD COLUMN project_id UUID NULL REFERENCES projects(id) ON DELETE CASCADE;

-- Hot path: "list credentials owned by this project". Partial index
-- so the global-rows case (project_id IS NULL — admin lookup) doesn't
-- pay the storage / write cost.
CREATE INDEX idx_dns_credentials_project
    ON dns_credentials(project_id)
    WHERE project_id IS NOT NULL;
