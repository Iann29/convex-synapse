-- v1.0 — project-level RBAC. Override layer on top of team_members.
--
-- Synapse used to scope roles purely at the team grain (admin / member).
-- That meant inviting a contractor to one project required giving them
-- access to every project the team owns. project_members fixes that:
--   - Each row is a (project_id, user_id) override. Role one of admin /
--     member / viewer.
--   - Resolution order at runtime: project_members.role wins; falls back
--     to team_members.role if no override exists.
--   - viewer is project-only — there's no team-level "viewer" today.
--
-- We don't yet support inviting users who aren't in the owning team.
-- The handler enforces "target must be a team_member" — that keeps team
-- as the security boundary while allowing per-project privilege drops
-- (team admin, but viewer on this project, etc.).
--
-- ON DELETE CASCADE on both FKs so deleting a project, a user, or
-- (transitively) a team cleans up the override rows automatically.

CREATE TABLE project_members (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('admin', 'member', 'viewer')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);

CREATE INDEX project_members_user_idx ON project_members (user_id);
