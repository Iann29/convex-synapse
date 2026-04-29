# CLAUDE.md

Project-level instructions for Claude Code when working in this repo.
A short, opinionated guide so a fresh Claude session can be productive
immediately. Keep it under ~200 lines.

## What this project is

**Synapse** is an open-source control plane for self-hosted Convex deployments
— it replicates the Cloud's "Big Brain" management layer (teams, projects,
multi-deployment) on infrastructure the user controls.

Big Brain itself is closed-source; we re-implement (a subset of) the public
[OpenAPI v1 spec](https://github.com/get-convex/convex-backend/blob/main/npm-packages/dashboard/dashboard-management-openapi.json)
that the Convex Cloud dashboard talks to. The end goal is that the Convex Cloud
dashboard (forked under `dashboard/`) can talk to Synapse with only the base
URL changed.

## Repo layout

| Path | What's there |
|---|---|
| `synapse/` | Go backend — REST API + Docker provisioner |
| `synapse/cmd/server/main.go` | Entrypoint |
| `synapse/internal/api/` | HTTP handlers (one file per resource: auth, teams, projects, deployments…) |
| `synapse/internal/auth/` | Password hash, JWT issuer, opaque-token helpers, request context |
| `synapse/internal/db/` | pgx pool + embedded SQL migrations |
| `synapse/internal/docker/` | Docker SDK wrapper + provisioner |
| `synapse/internal/middleware/` | chi middleware (auth, logging) |
| `synapse/internal/models/` | Domain types — JSON tags match OpenAPI v1 |
| `synapse/migrations/` *(deprecated)* | All migrations live in `internal/db/migrations/` (so `go:embed` works) |
| `dashboard/` | Will host the Next.js dashboard fork (placeholder for now) |
| `docs/` | ARCHITECTURE.md, ROADMAP.md, QUICKSTART.md |
| `docker-compose.yml` | Local dev stack (postgres + future synapse + dashboard) |
| `.env.example` | Every config var the backend reads |

## Common commands

Run from `synapse/` unless noted.

```bash
# DB up
docker compose -f ../docker-compose.yml up -d postgres

# Build & run server (loads ../.env automatically)
go build ./... && go vet ./...
go run ./cmd/server

# Reset DB tables (TRUNCATE — keeps schema)
PGPASSWORD=synapse psql -h localhost -U synapse -d synapse -c \
  "TRUNCATE users, teams, projects, team_members, deployments, project_env_vars, \
   team_invites, deploy_keys, access_tokens, audit_events RESTART IDENTITY;"

# Quick smoke test (registers a user, creates a team & project, sets env vars)
# See the test loops in commit messages — the canonical flow is documented there.
```

## Code conventions

- **Handlers** live under `internal/api/<resource>.go`. Each resource exposes a
  `Routes() chi.Router` method and a `Handler` struct holding deps. Mount in
  `router.go`.
- **DB access**: pgx directly (no ORM). Queries inline in handlers for v0.
  When a query is reused across handlers, lift it into the same package as
  a small helper (e.g. `loadProjectDeployments` in `projects.go`).
- **Errors to clients**: always go through `writeError(w, status, code, msg)`
  in `httpx.go`. The `code` is for programmatic checks; `message` is human-
  readable. Never leak internal error strings.
- **Auth context**: handlers under the auth group can call
  `auth.UserID(r.Context())`. Resource handlers should resolve the target
  (team/project/deployment) and assert membership/ownership in a single
  helper — see `loadTeamForRequest` in teams.go for the pattern.
- **Comments**: only when the *why* is non-obvious. We have a lot of WHY
  comments because this is greenfield and the trade-offs aren't yet
  documented anywhere else.

## DB schema philosophy

- UUIDs everywhere (`gen_random_uuid()`).
- `citext` for emails and slugs (no fights about casing).
- `ON DELETE CASCADE` for team→members→projects→deployments.
- Migrations are embedded into the binary via `go:embed` and run on startup.
  Never apply them out-of-band in prod — the binary is the source of truth.

## Testing

There is no formal test suite yet. The pattern so far is **end-to-end smoke
tests after each feature** using `curl` against a running server backed by
the local Postgres. Each commit message captures the manual test flow that
verified the feature.

Add real Go tests when:
- You touch business-logic that's not trivially obvious from the SQL/HTTP
  layer (e.g. provisioner state machines).
- A bug surfaces — add a test that fails on the bug, then fix.

## Versioning

- We're on `main`, no tags yet.
- Conventional commit prefixes: `feat(scope):`, `fix(scope):`, `chore:`,
  `docs:`. Scope is usually `synapse/<resource>`.
- Push to `origin/main` after each meaningful slice of work; the project
  is public.

## What NOT to add

This repo is intentionally **less** than Convex Cloud:

- No Stripe / Orb billing
- No WorkOS / SAML / OAuth login flows (just email+password JWT)
- No audit logging beyond the placeholder table
- No multi-region / deployment classes
- No Discord / Vercel integrations
- No LaunchDarkly feature flags

If you're tempted to add one of these, push back hard or move it to the
roadmap. The MVP is "user signs up, creates a team, creates a project,
provisions a deployment, Convex backend container runs". Anything that
isn't on that path can wait.

## Pointers

- North-star spec: `npm-packages/dashboard/dashboard-management-openapi.json`
  in `get-convex/convex-backend`.
- Self-hosted Convex docs: https://docs.convex.dev/self-hosting
- The `dashboard-self-hosted` package in convex-backend shows what a single-
  deployment dashboard looks like; we're swapping its `_app.tsx` stubs for
  real hooks pointed at Synapse.
