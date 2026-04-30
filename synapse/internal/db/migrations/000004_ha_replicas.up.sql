-- v0.5 HA-per-deployment foundation. Schema-only — no behavioral change
-- until the matching code lands behind SYNAPSE_HA_ENABLED. The migration
-- is additive: existing deployments continue to work unchanged, and we
-- backfill a single deployment_replicas row per existing deployment so the
-- new code can read replicas uniformly without special-casing.

-- ============================================================
-- Per-deployment storage configuration. One row per deployment that
-- chose Postgres + S3 instead of SQLite + local volume. Single-replica
-- SQLite deployments get NO row here.
--
-- Connection material (Postgres URL, S3 keys) is encrypted at rest using
-- AES-GCM keyed by SYNAPSE_STORAGE_KEY. The encryption helper lives in
-- internal/crypto/. Bytes column type is the simplest tamper-evident
-- envelope — schema does not need to know the cipher details.
-- ============================================================

CREATE TABLE deployment_storage (
    deployment_id      UUID PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,

    -- 'postgres' is the only meaningful value today; 'sqlite' is the
    -- "no storage row" signal but we keep the column so downgrade scripts
    -- have a clean inverse. CHECK lets us add 'mysql', 'cockroach', etc.
    -- later via a single migration instead of widening every query.
    db_kind            TEXT NOT NULL CHECK (db_kind IN ('postgres')),

    -- Encrypted POSTGRES_URL for the deployment. Includes user/password
    -- material — never logged, never returned over the API.
    db_url_enc         BYTEA NOT NULL,

    -- The deployment-specific database/schema name we created. Synapse
    -- runs `CREATE DATABASE convex_<name>` (or `CREATE SCHEMA`) at
    -- deployment-create-time; this column records what we chose so that
    -- delete-deployment can drop it again.
    db_schema          TEXT NOT NULL,

    -- S3-compatible object storage. Five buckets per deployment matching
    -- the upstream backend's expectations: files, modules, search,
    -- exports, snapshot_imports. Single endpoint + access pair shared
    -- across all five (this is how upstream's docker-compose.yml does it
    -- in practice, see self-hosted/docker/docker-compose.yml of
    -- get-convex/convex-backend).
    s3_endpoint        TEXT NOT NULL,
    s3_region          TEXT NOT NULL DEFAULT 'us-east-1',
    s3_access_key_enc  BYTEA NOT NULL,
    s3_secret_key_enc  BYTEA NOT NULL,
    s3_bucket_files    TEXT NOT NULL,
    s3_bucket_modules  TEXT NOT NULL,
    s3_bucket_search   TEXT NOT NULL,
    s3_bucket_exports  TEXT NOT NULL,
    s3_bucket_snapshots TEXT NOT NULL,

    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============================================================
-- One row per running container. For single-replica deployments today
-- (the only kind that exists pre-v0.5) we backfill one row per
-- deployment to keep the lookup uniform — `replica_index = 0` and the
-- existing host_port/container_id values copied across.
-- ============================================================

CREATE TABLE deployment_replicas (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_id   UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,

    -- Position within the deployment. 0 = first replica, 1 = second.
    -- v0.5 ships with replica_count <= 2; the column is smallint so
    -- bumping that later is free.
    replica_index   SMALLINT NOT NULL,

    container_id    TEXT,
    host_port       INTEGER,

    -- Independent of deployment.status — the deployment-level status is
    -- a roll-up. Replica goes provisioning → running, then either stays
    -- there or flips to stopped/failed. Health worker writes here.
    status          TEXT NOT NULL CHECK (status IN ('provisioning','running','stopped','failed')),

    -- Set by the proxy's health probe loop when it last saw a 2xx from
    -- this replica's lease holder (i.e. /api/check_admin_key succeeded).
    -- Used by the picker to prefer the most-recently-active replica.
    last_seen_active_at TIMESTAMPTZ,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (deployment_id, replica_index),
    -- Per-process-host port allocation continues to be globally unique —
    -- two replicas can't share a port, regardless of which deployment
    -- owns them. NULL is allowed (proxy mode + replica still booting).
    UNIQUE (host_port)
);

CREATE INDEX deployment_replicas_deployment_idx
    ON deployment_replicas (deployment_id);

CREATE INDEX deployment_replicas_status_idx
    ON deployment_replicas (status)
    WHERE status IN ('running','provisioning');

-- ============================================================
-- Deployment-level HA flags. Both default to "single-replica" so the
-- migration is a pure no-op for existing rows.
-- ============================================================

ALTER TABLE deployments
    ADD COLUMN ha_enabled BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN replica_count SMALLINT NOT NULL DEFAULT 1
        CHECK (replica_count BETWEEN 1 AND 8);

-- ============================================================
-- Backfill: every existing deployment becomes a single-replica row in
-- deployment_replicas, mirroring its current container_id/host_port.
-- Adopted deployments have neither (Synapse never created a container)
-- so they get a row with both NULL — the new code treats those as
-- "no infrastructure to manage" same as the deployments.go::adopted
-- branch already does at the row level.
-- ============================================================

INSERT INTO deployment_replicas (deployment_id, replica_index, container_id, host_port, status, created_at)
SELECT
    id,
    0,
    container_id,
    host_port,
    CASE WHEN status = 'deleted' THEN 'stopped' ELSE status END,
    created_at
FROM deployments
WHERE NOT EXISTS (
    SELECT 1 FROM deployment_replicas r WHERE r.deployment_id = deployments.id
);

-- We deliberately keep deployments.host_port + deployments.container_id
-- in place for now. Single-replica code paths still read them; the
-- replica-aware paths read from deployment_replicas. A later migration
-- will drop the deployments columns once nothing reads them. This
-- two-step removes any "big-bang" risk during the v0.5 rollout.
