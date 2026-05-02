-- v1.0 — extend the access_tokens.scope CHECK to allow 'app'.
-- 'app' tokens mirror Convex Cloud's `app_access_tokens` family: short-lived
-- per-project keys for CI/CD preview deploys. Behaviour-wise they're identical
-- to project-scoped tokens; the scope label is what the dashboard uses to
-- categorise them in the UI ("App tokens" vs "Project tokens").

ALTER TABLE access_tokens
    DROP CONSTRAINT access_tokens_scope_check;

ALTER TABLE access_tokens
    ADD CONSTRAINT access_tokens_scope_check
    CHECK (scope IN ('user','team','project','deployment','app'));
