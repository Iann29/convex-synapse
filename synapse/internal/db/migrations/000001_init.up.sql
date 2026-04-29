-- Synapse initial schema.
-- Greenfield project — one big migration is fine for v0; later changes get
-- their own files.

CREATE EXTENSION IF NOT EXISTS "pgcrypto";  -- gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS "citext";    -- case-insensitive text (emails, slugs)

-- ============================================================
-- Users / auth
-- ============================================================

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         CITEXT NOT NULL UNIQUE,
    password_hash TEXT   NOT NULL,
    name          TEXT   NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX users_email_idx ON users (email);

-- Personal access tokens. Used by the dashboard JWT refresh AND by tools like
-- `npx convex` for programmatic access. Scope determines what the token can do:
--   - 'user'       → full account access (dashboard sessions)
--   - 'team'       → all projects+deployments inside a single team
--   - 'project'    → all deployments inside a single project
--   - 'deployment' → operations against one deployment
CREATE TABLE access_tokens (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name          TEXT NOT NULL DEFAULT '',
    token_hash    TEXT NOT NULL UNIQUE,
    scope         TEXT NOT NULL CHECK (scope IN ('user','team','project','deployment')),
    scope_id      UUID,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ,
    last_used_at  TIMESTAMPTZ
);

CREATE INDEX access_tokens_user_idx ON access_tokens (user_id);
CREATE INDEX access_tokens_scope_idx ON access_tokens (scope, scope_id);

-- ============================================================
-- Teams
-- ============================================================

CREATE TABLE teams (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT   NOT NULL,
    slug            CITEXT NOT NULL UNIQUE,
    creator_user_id UUID   NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    default_region  TEXT   NOT NULL DEFAULT 'self-hosted',
    suspended       BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE team_members (
    team_id    UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('admin','member')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id)
);

CREATE INDEX team_members_user_idx ON team_members (user_id);

CREATE TABLE team_invites (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id     UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    email       CITEXT NOT NULL,
    role        TEXT NOT NULL CHECK (role IN ('admin','member')),
    invited_by  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token       TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    accepted_at TIMESTAMPTZ,
    UNIQUE (team_id, email)
);

-- ============================================================
-- Projects
-- ============================================================

CREATE TABLE projects (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id    UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name       TEXT   NOT NULL,
    slug       CITEXT NOT NULL,
    is_demo    BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (team_id, slug)
);

CREATE INDEX projects_team_idx ON projects (team_id);

-- Default env vars applied to deployments at creation.
-- `deployment_types` filters which types receive this var (e.g. {prod}).
CREATE TABLE project_env_vars (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    value            TEXT NOT NULL,
    deployment_types TEXT[] NOT NULL DEFAULT '{dev,prod,preview}',
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

-- ============================================================
-- Deployments
-- ============================================================

CREATE TABLE deployments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- Globally-unique short name (e.g. "quiet-cat-1234"). Used as the docker
    -- container name, the dashboard URL slug, and the INSTANCE_NAME env var.
    name            TEXT NOT NULL UNIQUE,
    deployment_type TEXT NOT NULL CHECK (deployment_type IN ('dev','prod','preview','custom')),
    status          TEXT NOT NULL CHECK (status IN ('provisioning','running','stopped','failed','deleted')),
    container_id    TEXT,
    host_port       INTEGER UNIQUE,
    deployment_url  TEXT,
    admin_key       TEXT NOT NULL,
    instance_secret TEXT NOT NULL,
    is_default      BOOLEAN NOT NULL DEFAULT false,
    reference       TEXT,
    creator_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_deploy_at  TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ
);

CREATE INDEX deployments_project_idx ON deployments (project_id);
CREATE INDEX deployments_status_idx ON deployments (status);

CREATE TABLE deploy_keys (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    name          TEXT NOT NULL DEFAULT '',
    token_hash    TEXT NOT NULL UNIQUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    last_used_at  TIMESTAMPTZ
);

CREATE INDEX deploy_keys_deployment_idx ON deploy_keys (deployment_id);

-- ============================================================
-- Audit log (placeholder — not actively written in v0)
-- ============================================================

CREATE TABLE audit_events (
    id          BIGSERIAL PRIMARY KEY,
    team_id     UUID REFERENCES teams(id) ON DELETE SET NULL,
    actor_id    UUID REFERENCES users(id) ON DELETE SET NULL,
    action      TEXT NOT NULL,
    target_type TEXT,
    target_id   UUID,
    metadata    JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX audit_events_team_idx ON audit_events (team_id, created_at DESC);
