-- v0.5 chunk 10: upgrade-to-HA job kind.
--
-- The provisioning_jobs.kind CHECK currently only allows 'provision'.
-- Widen to include 'upgrade_to_ha' so the new endpoint can enqueue a
-- job that the worker handles on a separate code path.
--
-- 'upgrade_to_ha' jobs walk a single-replica deployment through:
--   1. snapshot_export from the existing replica
--   2. provision 2 new HA replicas backed by Postgres + S3
--   3. snapshot_import into the new HA pair
--   4. atomic swap (flip ha_enabled, mark old replica stopped, swap
--      proxy address)
--
-- The endpoint reserves the API surface today; the worker's
-- step-2-through-4 mechanical work is bookmarked for a follow-up.

ALTER TABLE provisioning_jobs
    DROP CONSTRAINT provisioning_jobs_kind_check;

ALTER TABLE provisioning_jobs
    ADD CONSTRAINT provisioning_jobs_kind_check
    CHECK (kind IN ('provision', 'upgrade_to_ha'));
