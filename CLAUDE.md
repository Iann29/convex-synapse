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
| `synapse/cmd/server/main.go` | Entrypoint. Wires DB, JWT, docker, health worker, provisioner worker, optional reverse proxy, optional crypto box for HA |
| `synapse/internal/api/` | HTTP handlers (one file per resource: auth, teams, projects, deployments, invites, access_tokens, audit_log, health) |
| `synapse/internal/audit/` | Best-effort writer (`Record`) hooked into every mutating handler |
| `synapse/internal/auth/` | Password hash, JWT issuer, opaque-token helpers, request context |
| `synapse/internal/crypto/` | AES-256-GCM envelope (`SecretBox`) used for `deployment_storage` Postgres URL + S3 keys. v0.5+, opt-in via `SYNAPSE_STORAGE_KEY` |
| `synapse/internal/db/` | pgx pool + embedded SQL migrations + `WithRetryOnUniqueViolation` + `WithTryAdvisoryLock` helpers |
| `synapse/internal/docker/` | Docker SDK wrapper. `Provision[Replica]/Destroy[Replica]/Status[Replica]/Restart[Replica]/GenerateAdminKey`. v0.5 split single-replica vs HA paths via `DeploymentSpec.HAReplica` + `Storage` |
| `synapse/internal/health/` | Periodic worker that reconciles `deployment_replicas.status` with Docker reality, then rolls up to `deployments.status`. Optional auto-restart |
| `synapse/internal/middleware/` | chi middleware (auth, logging, CORS) |
| `synapse/internal/models/` | Domain types — JSON tags match OpenAPI v1. v0.5 added `Deployment.HAEnabled/ReplicaCount`, `DeploymentReplica`, `DeploymentStorage` |
| `synapse/internal/provisioner/` | Persistent job queue; `Worker` runs N parallel goroutines pulling via `SELECT FOR UPDATE SKIP LOCKED`. v0.5 reads `replica_id` and decrypts `deployment_storage` for HA jobs |
| `synapse/internal/proxy/` | Optional `/d/{name}/*` reverse proxy. v0.5 returns multi-replica address list and fails over on connection error |
| `synapse/internal/test/` | Integration test suite (`Setup(t)` / `SetupHA(t)` harness + per-resource `_test.go`). Package: `synapsetest` |
| `synapse/internal/db/migrations/` | `go:embed`'d SQL migrations applied at startup. Currently 6 migrations (init, jobs, adopted, replicas, replica_id on jobs, upgrade_to_ha kind) |
| `dashboard/` | Next.js 16 + Tailwind 4 dashboard. Real pages, not a placeholder. HA toggle + `HA ×N` badge on deployments since v0.5 |
| `dashboard/tests/` | Playwright e2e (20 specs) — runs against live compose stack |
| `setup.sh` | v0.6 auto-installer entry point. `main()` wrapper for curl-pipe-shell safety, ERR/EXIT traps, flock single-instance, full CLI flag surface |
| `installer/lib/` | Pure-bash detection helpers (detect:: namespace) — OS, arch, pkg manager, sudo, has_*, disk/RAM, public_ip |
| `installer/install/` | Phase scripts the orchestrator composes (preflight, secrets, caddy, compose, verify, ui) |
| `installer/templates/` | env.tmpl + caddy.fragment + caddy.standalone — rendered with `{{KEY}}` substitution from exported env vars |
| `installer/test/` | bats unit tests (~211 cases) + Dockerfile that adds jq+curl to bats/bats:latest |
| `docs/` | ARCHITECTURE, ROADMAP, QUICKSTART, API, DESIGN, V0_5_PLAN, V0_6_INSTALLER_PLAN, HA_TESTING, PRODUCTION |
| `docker-compose.yml` | Local dev stack: postgres + synapse + dashboard. Optional `ha` profile (backend-postgres + minio) and `caddy` profile (TLS reverse proxy) |
| `.env.example` | Every config var the backend reads, including the `SYNAPSE_HA_*` and `SYNAPSE_BACKEND_*` knobs |
| `.vps/` | **gitignored** — synapse-test VPS credentials + private SSH key. NEVER commit |

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
cd synapse && go test ./... -count=1            # ~18s, integration (136 tests)
cd dashboard && npx playwright test             # ~1.5min, against live stack
docker run --rm -v "$PWD:/code" -w /code synapse-bats -r installer/test/   # bats (211)
docker run --rm -v "$PWD:/mnt" -w /mnt koalaman/shellcheck:stable -x setup.sh installer/lib/*.sh installer/install/*.sh

# Real-VPS smoke test (when changes affect setup.sh / compose / Go API surface)
ssh synapse-vps      # Hetzner CPX22, Ubuntu 24.04, IP in /.vps/credentials.md
# inside the VPS:
cd /tmp && rm -rf convex-synapse && git clone -b <branch> https://github.com/Iann29/convex-synapse.git
cd convex-synapse && bash setup.sh --no-tls --skip-dns-check --non-interactive --install-dir=/opt/synapse-test
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
- `SetupHA(t)` (v0.5+) is the HA variant — same wiring plus a per-test
  `*crypto.SecretBox` and stub HA cluster config so HA flows can be
  exercised end-to-end against `FakeDocker`. The `Crypto` field on the
  Harness exposes the box for tests that want to decrypt
  `deployment_storage` rows directly.
- Package `synapsetest` (NOT whitebox) — harness imports `internal/api`,
  co-locating tests would create an import cycle.
- ~131 tests across auth/teams/projects/deployments/invites/health/proxy/
  audit/access_tokens/race/advisorylock/provisioner/HA.
- Dependency: postgres on `localhost:5432` OR `SYNAPSE_TEST_DB_URL`. Skips
  (doesn't fail) if no postgres is reachable.
- Decode every response with `json.DisallowUnknownFields()` so shape drift
  fails loudly.
- Real-backend HA test (`ha_real_e2e_test.go`) is gated by
  `SYNAPSE_HA_E2E=1`; skipped by default. See `docs/HA_TESTING.md` for
  the operator setup.

**Playwright e2e** (`dashboard/tests/`)

- 20 specs against the live compose stack at `localhost:6790` + `:8080`.
  ~2.5 min for the full suite.
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
| Adopt existing (register an external Convex backend) | `internal/api/deployments.go::adoptDeployment` |
| Pagination on listings (`?limit&?cursor` + `X-Next-Cursor`) | `internal/api/pagination.go` |
| `npx convex` CLI compatibility | `internal/api/deployments.go::deploymentCLICredentials` |
| Reverse proxy (`/d/{name}/*`, multi-replica failover) | `internal/proxy/` |
| Health worker (replica-aware aggregate roll-up) + auto-restart | `internal/health/` |
| Audit log (Cloud-vocabulary actions) | `internal/audit/`, `internal/api/audit_log.go` |
| Multi-node hygiene (retry / advisory lock / queue) | `internal/db/`, `internal/provisioner/` |
| **HA-per-deployment (v0.5)**: 2 replicas + Postgres + S3 backed, encrypted secrets, proxy failover, `upgrade_to_ha` endpoint reserved | `internal/crypto/`, `internal/db/migrations/000004-000006`, `internal/api/deployments.go::createDeployment ha:true`, `upgradeToHA` |
| **Auto-installer (v0.6)**: `./setup.sh --domain=<host>` brings up the stack on a fresh VPS in ~3 min. Preflight + idempotent secrets + Caddy auto-detection + compose up --build + post-install self-test. Real-VPS validated against Hetzner CPX22 | `setup.sh`, `installer/` |
| **Public URL rewrite (PR #10 + #23/#24/#25)**: `/auth`, `/cli_credentials`, `getDeployment`, `getProjectDeployment`, both `listDeployments`, `createDeployment`, `adoptDeployment` all return rewritten URLs (`<PublicURL>/d/<name>` or `<PublicURL>:<port>`) so remote browsers/CLIs reach a working address | `internal/api/deployments.go::publicDeploymentURL` |
| **Convex Dashboard hosted + auto-login (PR #26)**: clicking "Open dashboard" on a deployment row opens an iframe shell at `/embed/<name>` that loads the upstream `ghcr.io/get-convex/convex-dashboard` image and answers its `postMessage` handshake with adminKey + URL — operator lands on the data/functions/logs UI auto-logged. A Caddy sidecar (`convex-dashboard-proxy`) strips `X-Frame-Options` + `frame-ancestors` so the iframe renders | `dashboard/app/embed/[name]/page.tsx`, `installer/templates/convex-dashboard.caddyfile` |
| Dashboard (20 e2e tests, real pages, HA toggle + badge) | `dashboard/` |

## Real-VPS validation

Bats + Go tests run in CI. Neither catches certain bug classes:

- **bash `set -e` footguns** (e.g. `[[ -n "$X" ]] && cmd` aborting when test is false at end of function) — bats doesn't run with set -e inherited from a caller
- **`docker compose pull` on `build:` services** — bats mocks docker
- **camelCase vs snake_case API shapes** — bats stubs return whatever the test author thought was true
- **`NEXT_PUBLIC_*` build-arg vs runtime env** — Next.js inlines at build time; mocked tests don't notice
- **Missing host tools** (`jq`, `dig`, `dnsutils`) — base test image bundles them
- **Public-IP / DNS / TLS / Let's Encrypt** flows — only real with a real domain

For changes that touch `setup.sh`, `installer/`, `docker-compose.yml`, or any
backend handler that emits a URL, run `./setup.sh` end-to-end on `synapse-vps`
before declaring done. The chunk-7-of-v0.6.0 PR (#19) caught 6 such bugs that
all had green bats CI. Each one is now in regression tests; the lesson
generalizes: real-VPS validation is part of "done" for any change that crosses
the bats / Go-test boundary.

## Pointers

- North-star spec: `npm-packages/dashboard/dashboard-management-openapi.json`
  in `get-convex/convex-backend`.
- Convex backend lease (single-writer-per-deployment, design constraint):
  `crates/postgres/src/lib.rs:1738-1799` of the Convex repo. Active-passive
  HA per deployment is possible (Postgres + S3 + LB); active-active isn't.
- Self-hosted docs: https://docs.convex.dev/self-hosting
