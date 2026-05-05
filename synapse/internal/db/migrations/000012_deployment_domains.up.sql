-- v1.1+ — per-deployment custom domains. Lets an operator route a
-- domain like "api.fechasul.com.br" at a specific deployment's
-- backend or dashboard port. Co-exists with SYNAPSE_BASE_DOMAIN
-- wildcard (a deployment is reachable both at <name>.<base> AND at
-- any of its registered custom domains).
--
-- Resolution at proxy/TLS time will use the partial index on the
-- domain column scoped to status='active'.

CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE deployment_domains (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    -- citext: case-insensitive comparison; DNS hostnames are case-insensitive.
    domain CITEXT NOT NULL,
    -- 'api'       → backend container port (queries/mutations, http actions)
    -- 'dashboard' → dashboard container port (web UI)
    role TEXT NOT NULL CHECK (role IN ('api', 'dashboard')),
    -- 'pending' → just registered, awaiting first DNS verification
    -- 'active'  → A record points at expected VPS IP; TLS + routing allowed
    -- 'failed'  → DNS check failed (last_dns_error has details)
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'active', 'failed')),
    dns_verified_at TIMESTAMPTZ,
    last_dns_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- one domain can only ever point at one deployment.
    UNIQUE (domain)
);

CREATE INDEX idx_deployment_domains_deployment
    ON deployment_domains(deployment_id);

-- proxy/TLS lookup hot path: only 'active' rows; one row per active
-- domain (UNIQUE above already covers global uniqueness).
CREATE INDEX idx_deployment_domains_active
    ON deployment_domains(domain) WHERE status = 'active';
