# CLAUDE.md

Project-level instructions for Claude Code when working in this repo.
A short, opinionated guide so a fresh Claude session can be productive
immediately. Keep it under ~250 lines.

## What this project is

**Synapse** is an open-source control plane for self-hosted Convex deployments
— it replicates the Cloud's "Big Brain" management layer (teams, projects,
multi-deployment, audit log, CLI auth) on infrastructure the user controls.

Big Brain itself is closed-source; we re-implement (a subset of) the public
[OpenAPI v1 spec](https://github.com/get-convex/convex-backend/blob/main/npm-packages/dashboard/dashboard-management-openapi.json)
that the Convex Cloud dashboard talks to. The dashboard fork in `dashboard/`
talks to Synapse with the same shape Cloud uses.

## Repo layout

| Path | What's there |
|---|---|
| `synapse/cmd/server/main.go` | Entrypoint. Wires DB, JWT, docker, health worker, provisioner worker, optional reverse proxy |
| `synapse/internal/api/` | HTTP handlers (one file per resource: auth, teams, projects, deployments, invites, access_tokens, audit_log, health) |
| `synapse/internal/audit/` | Best-effort writer (`Record`) hooked into every mutating handler |
| `synapse/internal/auth/` | Password hash, JWT issuer, opaque-token helpers, request context |
| `synapse/internal/db/` | pgx pool + embedded SQL migrations + `WithRetryOnUniqueViolation` + `WithTryAdvisoryLock` helpers |
| `synapse/internal/docker/` | Docker SDK wrapper (Provision/Destroy/Status/Restart/GenerateAdminKey) |
| `synapse/internal/health/` | Periodic worker that reconciles `deployments.status` with Docker reality. Optional auto-restart |
| `synapse/internal/middleware/` | chi middleware (auth, logging, CORS) |
| `synapse/internal/models/` | Domain types — JSON tags match OpenAPI v1 |
| `synapse/internal/provisioner/` | Persistent job queue; `Worker` runs N parallel goroutines pulling via `SELECT FOR UPDATE SKIP LOCKED` |
| `synapse/internal/proxy/` | Optional `/d/{name}/*` reverse proxy mount, with a name→address resolver cache |
| `synapse/internal/test/` | Integration test suite (`Setup(t)` harness + per-resource `_test.go`). Package: `synapsetest` |
| `synapse/internal/db/migrations/` | `go:embed`'d SQL migrations applied at startup. Currently 2 migrations |
| `dashboard/` | Next.js 16 + Tailwind 4 dashboard. Real pages, not a placeholder |
| `dashboard/tests/` | Playwright e2e (16 specs) — runs against live compose stack |
| `docs/` | ARCHITECTURE.md, ROADMAP.md, QUICKSTART.md, API.md, DESIGN.md |
| `docker-compose.yml` | Local dev stack: postgres + synapse + dashboard |
| `.env.example` | Every config var the backend reads |

## Common commands

```bash
# Bring up the full stack
docker compose up -d

# Rebuild + restart synapse only
docker compose build synapse && docker compose up -d synapse

# Reset DB tables (TRUNCATE — keeps schema)
PGPASSWORD=synapse psql -h localhost -U synapse -d synapse -c \
  "TRUNCATE users, teams, projects, team_members, deployments, project_env_vars, \
   team_invites, deploy_keys, access_tokens, audit_events, provisioning_jobs \
   RESTART IDENTITY;"

# Wipe provisioned containers + volumes
docker rm -f $(docker ps -aq --filter label=synapse.managed=true)
docker volume ls -q --filter name=synapse-data- | xargs -r docker volume rm

# Tests
cd synapse && go test ./... -count=1            # ~10s, integration
cd dashboard && npx playwright test             # ~1.5min, against live stack
```

## Code conventions

- **Handlers** live under `internal/api/<resource>.go`. Each resource exposes a
  `Routes() chi.Router` method and a `Handler` struct holding deps. Mount in
  `router.go`.
- **DB access**: pgx directly (no ORM). Queries inline in handlers.
- **Errors to clients**: always go through `writeError(w, status, code, msg)`
  in `httpx.go`. The `code` is for programmatic checks; `message` is human-
  readable. Never leak internal error strings or constraint names.
- **Auth context**: handlers under the auth group call `auth.UserID(r.Context())`.
  Resource handlers resolve the target (team/project/deployment) and assert
  membership/ownership in a single helper — see `loadTeamForRequest` in `teams.go`.
- **Audit**: any mutating handler calls `audit.Record(ctx, db, audit.Options{...})`
  on the success path. Best-effort — never fails the user's request.
- **Comments**: only when the *why* is non-obvious. Greenfield project, lots of
  trade-offs that aren't documented anywhere else.

## Multi-node patterns (v0.3 hygiene)

The codebase is safe to run with N processes against one Postgres + one Docker
daemon. The patterns:

- **Resource allocation** (port, deployment name, slug): wrap SELECT-then-INSERT
  in `db.WithRetryOnUniqueViolation(ctx, n, fn)`. UNIQUE constraint catches
  the race; retry generates a fresh candidate. See `deployments.go::createDeployment`.
- **PgError detection**: `db.IsUniqueViolation(err)` / `IsUniqueViolationOn(err, name)`
  — by SQLSTATE 23505 + constraint name. NEVER `strings.Contains` on error
  messages.
- **Periodic workers** (sweeps): wrap each tick in
  `db.WithTryAdvisoryLock(ctx, pool, key, fn)`. Single-node always acquires;
  multi-node coordinates so exactly one runs the work. Keys are constants in
  `db/advisorylock.go`.
- **Long-running async work**: enqueue a row in a job table, run a `Worker`
  with `SELECT FOR UPDATE SKIP LOCKED` and `Concurrency` parallel goroutines.
  See `provisioner.Worker`. Don't spawn `go someAsyncWork()` from a handler.
- **Caches in memory** (e.g. `proxy.Resolver`): assume per-node, set a TTL,
  expose `Invalidate(name)` for the rare case where another node's mutation
  needs immediate visibility. Don't try to share across nodes.

## DB schema philosophy

- UUIDs everywhere (`gen_random_uuid()`).
- `citext` for emails and slugs (no fights about casing).
- `ON DELETE CASCADE` for team→members→projects→deployments→provisioning_jobs.
- Migrations are embedded into the binary via `go:embed` and run on startup.
  `golang-migrate` already handles multi-node migration locking via the
  `schema_migrations` table — don't reinvent it.

## Testing

Both suites green on every push.

**Go integration** (`synapse/internal/test/`)

- `Setup(t)` builds a fresh `synapse_test_<hex>` database, applies migrations,
  wires the chi router with a `FakeDocker` + a `provisioner.Worker` (50ms poll),
  and returns a `httptest.Server`. ~470ms warm.
- Package `synapsetest` (NOT whitebox) — harness imports `internal/api`,
  co-locating tests would create an import cycle.
- ~100 tests across auth/teams/projects/deployments/invites/health/proxy/
  audit/access_tokens/race/advisorylock/provisioner.
- Dependency: postgres on `localhost:5432` OR `SYNAPSE_TEST_DB_URL`. Skips
  (doesn't fail) if no postgres is reachable.
- Decode every response with `json.DisallowUnknownFields()` so shape drift
  fails loudly.

**Playwright e2e** (`dashboard/tests/`)

- 16 specs against the live compose stack at `localhost:6790` + `:8080`.
  ~1.5 min for the full suite.
- Direct postgres + Docker SDK for cleanup helpers in `tests/helpers/`.
- All locators use stable IDs (`#register-email`) or roles. Avoid `getByText`
  with regex — those collide on partial matches; scope to `getByRole("dialog")`
  inside modals.

CI runs both jobs (plus `go build`, `go vet`, `npm run build`, compose build)
on every push.

## Versioning

- `main` only, no tags yet.
- Conventional commit prefixes: `feat(scope):`, `fix(scope):`, `chore:`,
  `docs:`, `test(...)`. Scope: `synapse/<resource>` or `synapse/<package>`.
- Push to `origin/main` after each meaningful slice; the project is public.

## What NOT to add

Synapse is intentionally **less** than Convex Cloud:

- No Stripe / Orb billing
- No WorkOS / SAML / SAML-OAuth flows (email+password JWT only; OIDC v1.0+)
- No multi-region / deployment classes
- No Discord / Vercel integrations
- No LaunchDarkly feature flags

If you're tempted to add one, move it to the roadmap and discuss first.

## What HAS landed

| Feature | Where |
|---|---|
| Auth (register/login/refresh, JWT + opaque PATs) | `internal/api/auth.go`, `internal/api/access_tokens.go` |
| Teams + invites (multi-user via opaque tokens) | `internal/api/teams.go`, `internal/api/invites.go` |
| Projects + env vars (CRUD, batch updates) | `internal/api/projects.go` |
| Deployments (provision real Convex backends, ~1s) | `internal/api/deployments.go`, `internal/provisioner/` |
| `npx convex` CLI compatibility | `internal/api/deployments.go::deploymentCLICredentials` |
| Reverse proxy (`/d/{name}/*`) | `internal/proxy/` |
| Health worker + auto-restart | `internal/health/` |
| Audit log (Cloud-vocabulary actions) | `internal/audit/`, `internal/api/audit_log.go` |
| Multi-node hygiene (retry / advisory lock / queue) | `internal/db/`, `internal/provisioner/` |
| Dashboard (16 e2e tests, real pages) | `dashboard/` |

## Pointers

- North-star spec: `npm-packages/dashboard/dashboard-management-openapi.json`
  in `get-convex/convex-backend`.
- Convex backend lease (single-writer-per-deployment, design constraint):
  `crates/postgres/src/lib.rs:1738-1799` of the Convex repo. Active-passive
  HA per deployment is possible (Postgres + S3 + LB); active-active isn't.
- Self-hosted docs: https://docs.convex.dev/self-hosting
