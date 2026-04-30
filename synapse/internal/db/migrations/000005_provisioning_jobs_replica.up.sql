-- v0.5 chunk 6/7: replica-targeted provisioning jobs.
--
-- Pre-v0.5 jobs target a deployment ("provision this deployment as a
-- single SQLite + local-volume container"). HA jobs target a specific
-- replica row ("provision replica 0 of this deployment, with this
-- container name + this Postgres URL + these S3 buckets").
--
-- replica_id is nullable: legacy jobs left over from before this
-- migration ran, or v0.5 single-replica deployments that don't bother
-- writing the column, both keep working.

ALTER TABLE provisioning_jobs
    ADD COLUMN replica_id UUID REFERENCES deployment_replicas(id) ON DELETE CASCADE;

CREATE INDEX provisioning_jobs_replica_idx
    ON provisioning_jobs (replica_id)
    WHERE replica_id IS NOT NULL;
