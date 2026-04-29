-- Provisioning queue. The `create_deployment` handler used to spawn an
-- in-process goroutine that called Docker.Provision and updated the row
-- when it finished. That goroutine died with the process — if Synapse
-- crashed mid-provision the deployment row was stuck in 'provisioning'
-- and a 10-minute orphan-sweep was the only recovery path.
--
-- Persisting work-to-do as rows lets any node in the fleet pick the next
-- job via SELECT … FOR UPDATE SKIP LOCKED and lets a process restart
-- recover claimed-but-incomplete jobs by re-pending claims older than
-- the configured timeout.

CREATE TABLE provisioning_jobs (
    id            BIGSERIAL PRIMARY KEY,
    deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,

    -- kind currently only 'provision'; reserved column lets us add
    -- 'restart', 'destroy', 'snapshot', etc. later without a migration.
    kind          TEXT NOT NULL CHECK (kind IN ('provision')),

    -- Lifecycle:
    --   pending → claimed → done
    --                     ↘ failed
    -- A worker FOR UPDATE SKIP LOCKED's a 'pending' row, immediately
    -- updates it to 'claimed' with claimed_by + claimed_at, then runs
    -- the docker work outside the txn. On finish: 'done' or 'failed'.
    status        TEXT NOT NULL CHECK (status IN ('pending','claimed','done','failed')),

    claimed_by    TEXT,
    claimed_at    TIMESTAMPTZ,
    finished_at   TIMESTAMPTZ,
    error         TEXT,
    attempts      INTEGER NOT NULL DEFAULT 0,

    -- Spec carried on the job row so the worker doesn't need to re-derive
    -- per-deployment behaviour from config (which may have changed since
    -- the job was enqueued).
    healthcheck_via_network BOOLEAN NOT NULL DEFAULT false,

    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Pending lookup is the hot path for the worker. Partial index keeps it
-- tiny — done/failed rows accumulate but don't slow polls.
CREATE INDEX provisioning_jobs_pending_idx
    ON provisioning_jobs (created_at)
    WHERE status = 'pending';

-- Quick lookup of a deployment's most recent job (used by the recovery
-- step to verify a stuck deployment has a job we can re-pend).
CREATE INDEX provisioning_jobs_deployment_idx
    ON provisioning_jobs (deployment_id, created_at DESC);
