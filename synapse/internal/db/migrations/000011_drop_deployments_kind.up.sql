-- v1.1+ — Aster integration removed. The `kind` column added in 000010
-- distinguished `convex` (default) from `aster`, but Aster never ran in
-- production and the integration is now removed from the codebase. Drop
-- the column. The constraint is implicitly dropped with the column.
ALTER TABLE deployments DROP COLUMN kind;
