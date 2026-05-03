-- v1.1+ — distinguish what *kind* of runtime backs a deployment.
--
-- Until now every deployment row was implicitly a Convex backend
-- container. We're starting to register a second kind, "aster", which
-- represents a future Aster runner cell (capsule-based execution plane,
-- see https://github.com/Iann29/aster). The aster image is not yet
-- released — kind='aster' rows are bookkeeping only: Synapse owns the
-- name/project/RBAC, but no container is provisioned and no proxy is
-- wired. Existing rows are backfilled to kind='convex' by the DEFAULT.
--
-- Putting the field on `deployments` (rather than a sibling table) keeps
-- list/get queries cheap and matches how `deployment_type` already lives.

ALTER TABLE deployments
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'convex';

ALTER TABLE deployments
    ADD CONSTRAINT deployments_kind_check
        CHECK (kind IN ('convex', 'aster'));
