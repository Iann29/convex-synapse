-- Reversal: re-add the kind column with default + check constraint.
-- Existing rows backfill to 'convex' via the DEFAULT.
ALTER TABLE deployments
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'convex';
ALTER TABLE deployments
    ADD CONSTRAINT deployments_kind_check
        CHECK (kind IN ('convex', 'aster'));
