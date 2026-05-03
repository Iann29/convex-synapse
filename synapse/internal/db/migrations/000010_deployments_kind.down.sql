ALTER TABLE deployments
    DROP CONSTRAINT IF EXISTS deployments_kind_check;

ALTER TABLE deployments
    DROP COLUMN IF EXISTS kind;
