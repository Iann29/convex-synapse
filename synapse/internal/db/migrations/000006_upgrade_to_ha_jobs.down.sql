ALTER TABLE provisioning_jobs
    DROP CONSTRAINT provisioning_jobs_kind_check;

ALTER TABLE provisioning_jobs
    ADD CONSTRAINT provisioning_jobs_kind_check
    CHECK (kind IN ('provision'));
