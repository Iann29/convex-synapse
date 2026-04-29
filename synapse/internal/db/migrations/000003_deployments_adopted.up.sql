-- Adopted deployments are existing Convex backends (already running on
-- some other host or container) that the operator has registered into
-- Synapse without going through the provisioner. The row points at an
-- external URL and admin key, and Synapse never tries to start, restart,
-- or destroy the underlying container.
--
-- Why a flag (and not "instance_secret IS NULL")? Adopted deployments do
-- not have an instance_secret we can sign things with — but neither does
-- a half-failed provision in some edge cases, and we don't want to widen
-- "no secret" into a behavioural switch by accident. A dedicated flag is
-- one bit and the intent is unambiguous.

ALTER TABLE deployments
    ADD COLUMN adopted BOOLEAN NOT NULL DEFAULT false;
