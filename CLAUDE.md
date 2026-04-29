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
| `synapse/internal/api/` | HTTP handlers (one file per resource: auth, teams, projects, deployments, invites, access_tokens…) |
| `synapse/internal/auth/` | Password hash, JWT issuer, opaque-token helpers, request context |
| `synapse/internal/db/` | pgx pool + embedded SQL migrations |
| `synapse/internal/docker/` | Docker SDK wrapper + provisioner. The api package depends on the `Provisioner` interface (defined in api/deployments.go) so tests can inject a fake. |
| `synapse/internal/health/` | Periodic worker that reconciles deployments.status with Docker reality |
| `synapse/internal/middleware/` | chi middleware (auth, logging, CORS) |
| `synapse/internal/models/` | Domain types — JSON tags match OpenAPI v1 |
| `synapse/internal/test/` | Integration test suite (`Setup(t)` harness + per-resource `_test.go`). Package: `synapsetest`. |
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

Two suites, both expected to be green on every push:

**Go integration tests** (`synapse/internal/test/`)

- `Setup(t)` builds a fresh `synapse_test_<hex>` postgres database, applies
  migrations, wires the chi router with a `FakeDocker` (satisfies `api.Provisioner`),
  and returns a `httptest.Server`. ~470ms warm; ~7s for the full suite.
- Tests live in package `synapsetest` (NOT whitebox in `internal/api`) because
  the harness imports `internal/api` to build the router — co-locating tests
  with handlers would create an import cycle.
- 44+ tests across auth/teams/projects/deployments/invites/health.
- Dependency: postgres on `localhost:5432` OR `SYNAPSE_TEST_DB_URL` env var.
  Tests SKIP (don't fail) if no postgres is reachable.
- Decode every response with `json.DisallowUnknownFields()` so shape drift
  fails loudly.
- Add a new test when: you touch business-logic, a bug surfaces, or you add
  a new endpoint.

**Playwright e2e** (`dashboard/tests/`)

- Runs against the live compose stack at `localhost:6790` + `localhost:8080`.
- 11+ tests, ~35s. Covers auth, teams, projects (rename/delete), env vars,
  deployments (create/delete/copy URL), multi-context invites.
- Direct postgres access at `localhost:5432` for `truncateAll()` reset
  between tests.
- Direct Docker SDK for `pruneSynapseContainers()` cleanup.
- All locators use stable IDs (`#register-email`) or roles. Avoid `getByText`
  with regex — those collide on partial matches.

CI runs both jobs (plus build + vet) on every push.

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
roadmap.

## What HAS landed

The MVP is "user signs up, creates a team, creates a project, provisions
a deployment, Convex backend container runs" — that path works end-to-end.
Beyond that, v0.1 also includes:

- Multi-user team membership via opaque invite tokens (`/v1/team_invites/accept`)
- Project rename + delete (cascades to deployments + env vars)
- Per-project default env vars (set/delete batch)
- Personal access tokens (CLI/CI authentication, `syn_*` opaque format)
- Async provisioning + status polling on the dashboard (UI updates live)
- Health worker that reconciles deployments.status with Docker reality
- Full Playwright e2e + Go integration test suites in CI

## Pointers

- North-star spec: `npm-packages/dashboard/dashboard-management-openapi.json`
  in `get-convex/convex-backend`.
- Self-hosted Convex docs: https://docs.convex.dev/self-hosting
- The `dashboard-self-hosted` package in convex-backend shows what a single-
  deployment dashboard looks like; we're swapping its `_app.tsx` stubs for
  real hooks pointed at Synapse.
