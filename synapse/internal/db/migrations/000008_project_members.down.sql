-- Down: revert project-level RBAC. Override rows go away; access falls
-- back to pure team_members semantics (which is where v1.0-pre Synapse
-- was). Safe — no team-level role data is touched.

DROP INDEX IF EXISTS project_members_user_idx;
DROP TABLE IF EXISTS project_members;
