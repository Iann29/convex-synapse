-- v1.0.3 — repurpose the orphaned deploy_keys table for the dashboard's
-- "+ Create Deploy Key" UX (mirrors Convex Cloud's Personal Deployment
-- Settings → Deploy Keys).
--
-- Pre-1.0.3 history: deploy_keys was created in migration 000001 but
-- never wired into the dashboard. The createDeployKey handler issued
-- opaque Synapse tokens that nothing read back. We reuse the table
-- instead of adding a parallel one — a deploy key is, conceptually, a
-- named per-deployment credential, which is exactly what this row
-- already represents. The fields just change shape:
--
--   token_hash     → admin_key_hash   (sha256 of the actual admin key)
--   (new column)     admin_key_prefix (first 8 chars after "<name>|" — display)
--   (new column)     revoked_at       (NULL = active)
--
-- IMPORTANT: revoke is best-effort — the Convex backend authenticates
-- admin keys by signature against INSTANCE_SECRET (stateless), so we
-- cannot per-key revoke without rotating the deployment's instance
-- secret. revoked_at hides the row from the dashboard list; real
-- invalidation requires a deployment-wide rotation. The dashboard
-- surfaces that gotcha. A future "tier 2" with Synapse in the request
-- path would close the gap.
--
-- The pre-existing UNIQUE constraint on token_hash was a global
-- collision check. Hashes of admin keys (which embed the deployment
-- name) won't collide across deployments, so a per-deployment partial
-- unique on (deployment_id, name) is what we actually want — operator
-- can reuse "vercel" after revoking the previous "vercel" key.

ALTER TABLE deploy_keys
    DROP CONSTRAINT deploy_keys_token_hash_key;

ALTER TABLE deploy_keys
    RENAME COLUMN token_hash TO admin_key_hash;

ALTER TABLE deploy_keys
    ADD COLUMN admin_key_prefix TEXT NOT NULL DEFAULT '',
    ADD COLUMN revoked_at       TIMESTAMPTZ;

CREATE UNIQUE INDEX deploy_keys_active_name
    ON deploy_keys (deployment_id, name)
    WHERE revoked_at IS NULL;
