ALTER TABLE deployments
    DROP COLUMN replica_count,
    DROP COLUMN ha_enabled;

DROP TABLE deployment_replicas;
DROP TABLE deployment_storage;
