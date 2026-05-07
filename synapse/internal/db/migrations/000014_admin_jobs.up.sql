-- v1.4+ — admin_jobs is a tiny work-queue for instance-level operations
-- that need to run on the host (outside the synapse-api container) via
-- the synapse-updater systemd daemon. The first user is the
-- "reconfigure host domain" flow: changing the URL where the dashboard
-- itself lives requires re-rendering Caddy + restarting the stack, and
-- only the on-host updater can do that without killing the api process
-- mid-job.
--
-- Schema is intentionally separate from provisioning_jobs because:
--  - lifecycle is different (admin_jobs are observable by the dashboard
--    via their own /status endpoint; provisioning_jobs are internal),
--  - retention/blast-radius differ (these are host-wide ops, not
--    per-deployment), and
--  - searching by `kind` for "the most recent host-domain change" is
--    cleaner against a dedicated table than filtering provisioning_jobs.

CREATE TABLE admin_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Whitelist of supported kinds. Add a new value via a follow-up
    -- migration when a new admin op lands; the daemon-side dispatcher
    -- enforces the same set so a row with an unknown kind goes nowhere.
    kind TEXT NOT NULL CHECK (kind IN ('reconfigure_host_domain')),
    -- payload is the validated, normalised request body the daemon
    -- needs to run the op (domain, baseDomain, plainHttp, acmeEmail).
    -- We persist it so the dashboard can show "what was requested" if
    -- the operator re-opens the panel mid-run.
    payload JSONB NOT NULL,
    state TEXT NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'running', 'succeeded', 'failed')),
    -- Captured stdout+stderr from the daemon's child process. Truncated
    -- on the read path if needed; we don't cap at write time so a long
    -- failure log stays inspectable when the operator re-opens the
    -- panel.
    log TEXT NOT NULL DEFAULT '',
    -- created_by points at the user who triggered the change. ON DELETE
    -- SET NULL keeps the audit trail intact if that user is later
    -- removed from the instance.
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    -- Short human-readable failure reason. Long stack/log lives in
    -- `log`; this column is what the dashboard renders in a banner.
    error TEXT
);

-- Hot path: "is anything in flight?" — partial index keeps it tiny
-- because rows live in queued/running for seconds, terminal for the
-- table's lifetime.
CREATE INDEX idx_admin_jobs_state ON admin_jobs(state)
    WHERE state IN ('queued', 'running');
