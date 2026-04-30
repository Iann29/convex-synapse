DROP INDEX IF EXISTS provisioning_jobs_replica_idx;
ALTER TABLE provisioning_jobs DROP COLUMN replica_id;
