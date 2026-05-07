-- v1.5+ — DNS-provider credentials. Lets an operator save a Cloudflare
-- (or future Route53/etc.) API token so Synapse can create the A record
-- that points a per-deployment custom domain at the host's public IP,
-- instead of asking the operator to SSH into Cloudflare's dashboard
-- and click around.
--
-- The token is encrypted at rest with the existing AES-GCM SecretBox
-- (SYNAPSE_STORAGE_KEY) — the same envelope HA's deployment_storage
-- uses for Postgres + S3 secrets. A stolen DB without the key yields
-- no usable Cloudflare credential.
--
-- `zones` mirrors the list of zones the token has access to (cached
-- from Cloudflare's /zones endpoint at save time) so the dashboard can
-- render "this token covers: [...]" without re-calling Cloudflare on
-- every page load. Refreshing the cache is left as an explicit
-- operator action (re-add the credential).

CREATE TABLE dns_credentials (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Whitelist of supported providers. Add a new value via a
    -- follow-up migration when a new provider lands.
    provider TEXT NOT NULL CHECK (provider IN ('cloudflare')),
    label TEXT NOT NULL,
    token_encrypted BYTEA NOT NULL,
    -- Zones the token has access to, cached at save time. Shape:
    --   [{"id": "abc...", "name": "fechasul.com.br"}, ...]
    -- The dashboard joins this against the apex of a custom domain
    -- to suggest "use this credential" without an extra round-trip.
    zones JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- last_used_at + last_error are observability fields populated by
    -- the auto-configure handler. Surfaced to the dashboard so
    -- operators can spot a revoked token before they need it.
    last_used_at TIMESTAMPTZ,
    last_error TEXT
);

CREATE INDEX idx_dns_credentials_provider ON dns_credentials(provider);

ALTER TABLE deployment_domains
    ADD COLUMN auto_configured BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN dns_credential_id UUID REFERENCES dns_credentials(id) ON DELETE SET NULL;

-- Hot path: "delete the credential when nothing references it" — the
-- DELETE handler reads this via EXISTS to return 409 credential_in_use
-- before attempting the delete. Partial index keeps it tiny.
CREATE INDEX idx_deployment_domains_credential
    ON deployment_domains(dns_credential_id) WHERE dns_credential_id IS NOT NULL;
