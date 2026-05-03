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
| `synapse/internal/db/migrations/` | `go:embed`'d SQL migrations applied at startup. v1.0.3+: 9 migrations (init, jobs, adopted, replicas, replica_id on jobs, upgrade_to_ha kind, app token scope, project_members RBAC, deploy_keys repurpose) |
| `dashboard/` | Next.js 16 + Tailwind 4 dashboard. Real pages, not a placeholder. HA toggle + `HA ×N` badge on deployments since v0.5 |
| `dashboard/tests/` | Playwright e2e (24 specs) — runs against live compose stack |
| `dashboard/app/setup/` | v0.6.3 first-run wizard. `/login` redirects here when `/v1/install_status` reports `firstRun=true` |
| `setup.sh` | v0.6 auto-installer entry point. `main()` wrapper for curl-pipe-shell safety, bootstrap re-exec under `curl \| bash`, ERR/EXIT traps, flock single-instance, full CLI flag surface (install + lifecycle) |
| `installer/lib/` | Pure-bash detection helpers (detect:: namespace) — OS, arch, pkg manager, sudo, has_*, disk/RAM, public_ip |
| `installer/install/` | Phase scripts the orchestrator composes (preflight, secrets, caddy, compose, verify, ui) + `lifecycle.sh` (upgrade / backup / restore / uninstall / logs / status) + `wizard.sh` (interactive Q&A walkthrough fired when no mode flags passed) + `updater.sh` (phase_install_updater, drops the v1.1.0+ self-update systemd daemon) |
| `installer/updater/` | Self-update daemon bundle — `synapse-updater` (Python 3 HTTP server on a unix socket) + `synapse-updater.service` (systemd unit). Lives on the host, not in docker compose, so it survives the upgrades it orchestrates |
| `installer/templates/` | env.tmpl + caddy.fragment + caddy.standalone — rendered with `{{KEY}}` substitution from exported env vars |
| `installer/test/` | bats unit tests (327 cases) + Dockerfile that adds jq+curl to bats/bats:latest |
| `docs/` | ARCHITECTURE, ROADMAP, QUICKSTART, API, DESIGN, HA_TESTING, PRODUCTION, SCREENSHOTS |
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
cd synapse && go test ./... -count=1            # ~18s, integration (146 tests)
cd dashboard && npx playwright test             # ~2.5min, against live stack (24 specs)
docker run --rm -v "$PWD:/code" -w /code synapse-bats -r installer/test/   # bats (305)
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
| **Lifecycle commands (v0.6.1)**: `setup.sh` exposes `--upgrade` (auto-detect via GitHub Releases /latest, snapshot-rollback on failure, slashes sanitized in stamps), `--backup` (manifest.txt + .env + compose + pg_dump + per-deployment volumes → tarball; `--exclude-env` opt-out), `--restore=<archive>` (wipes pgdata + per-deployment volumes by suffix-match; replays dump after a `SELECT 1` readiness gate; `--keep-env` opt-out), `--uninstall` (mandatory pre-uninstall backup unless `--skip-backup`; wipes volumes by default unless `--keep-volumes`; strips host-Caddy managed block), `--logs=<component> [--follow] [--tail=<n>]` (thin pass-through with strict component validation), `--status` (read-only diagnostic table). All real-VPS validated on `synapse-vps`. Audit trail per command in `$INSTALL_DIR/{upgrade,backup,restore}.log` | `installer/install/lifecycle.sh`, `setup.sh::main` early-return branches |
| **Hosted `curl \| sh` install (v0.6.2)**: `curl -sSf https://raw.githubusercontent.com/Iann29/convex-synapse/main/setup.sh \| bash -s -- --domain=...` boots the script straight from GitHub. `setup::needs_bootstrap` detects the `installer/`-not-on-disk case (BASH_SOURCE[0] is empty under `curl \| bash`); `setup::bootstrap` clones the repo into `/tmp/convex-synapse-bootstrap-<pid>` (or `~/.synapse-bootstrap-<pid>`) and `exec`s the cloned `setup.sh` with the original args. `--no-bootstrap` opts out for tests. `SYNAPSE_BOOTSTRAP_REF` env pins the ref (default: `main`) | `setup.sh::setup::needs_bootstrap`, `setup.sh::setup::bootstrap` |
| **First-run wizard (v0.6.3)**: dashboard `/login` probes `GET /v1/install_status` (public, no auth, `EXISTS` check on `users` table) and replaces to `/setup` when `firstRun=true`. The 4-phase wizard (loading → admin → demo → provisioning → done) creates the admin via `/v1/auth/register`, bootstraps `Default` team + `demo` project + dev deployment, then drops on the project page with the deployment row visible. `setup.sh::phase_verify` now `TRUNCATE users CASCADE` after the self-test passes (ON DELETE RESTRICT on `teams.creator_user_id` blocks row-level deletes; CASCADE follows the FK tree) so a fresh install lands at zero-user state and the wizard fires | `synapse/internal/api/install_status.go`, `dashboard/app/setup/page.tsx`, `dashboard/app/login/page.tsx::useEffect`, `installer/install/verify.sh` cleanup |
| **Custom domains with auto-TLS (v1.0 chunks 1+2)**: `SYNAPSE_BASE_DOMAIN=<host>` → deployment URLs become `https://<name>.<host>` instead of `<host>/d/<name>/`. Backend: `publicDeploymentURL` rewrites; proxy `Handler` accepts Host-header routing alongside the legacy path form; `/v1/internal/tls_ask` (public, no auth) gates Caddy on-demand TLS — returns 200 only for `<sub>.<base>` where `<sub>` is a real, non-deleted deployment. Installer: `setup.sh --base-domain=<host>` + env.tmpl + DNS preflight (`check_base_domain` synthetic-subdomain probe) + new `caddy.wildcard` template appended to standalone + host-Caddy fragment + standalone global gets `on_demand_tls { ask http://synapse-api:8080/v1/internal/tls_ask }`. Real-VPS validation needs operator-side wildcard DNS first (`*.<base>` A record → VPS IP) | `synapse/internal/api/{deployments.go,tls_ask.go,router.go}`, `synapse/internal/proxy/proxy.go::matchHostSubdomain`, `setup.sh --base-domain`, `installer/templates/caddy.{standalone,wildcard}`, `installer/install/preflight.sh::check_base_domain` |
| **OpenAPI 100% of self-hosted-relevant subset (v1.0)**: 12 new handlers — `transferProject`, `update_project` slug, `update_team`, `delete_team`, `update_member_role`, `remove_member`, `update_profile_name`, `delete_account`, `member_data`, `optins` — plus a `not_supported_in_self_hosted` middleware that 404s ~60 cloud-only paths (Stripe / WorkOS / Discord / Vercel / oauth_apps / cloud_backups / referrals). Scoped access tokens (user / team / project / app / deployment) with hierarchy enforcement at every `load*ForRequest`. | `synapse/internal/api/{projects.go,teams.go,me.go,access_tokens.go,not_supported.go,scope.go}`, `synapse/internal/db/migrations/000007_app_token_scope` |
| **In-header deployment picker (v1.0+ Strategy E)**: green pill above the iframed Convex Dashboard at `/embed/<name>` with grouped dropdown (Production / Development / Preview), Ctrl+Alt+1/2 hotkeys. Switch via `router.push("/embed/<new>")` (no upstream fork). Reserved cross-origin endpoint `/v1/internal/list_deployments_for_dashboard?token=<...>` for a future Strategy B that would render the picker INSIDE the iframe. | `dashboard/components/DeploymentPicker.tsx`, `dashboard/app/embed/[name]/page.tsx`, `synapse/internal/api/dashboard_proxy.go` |
| **Project-level RBAC (v1.0+)**: `project_members` override layer (admin / member / viewer) on top of `team_members`. `effectiveProjectRole(ctx, db, projectID, teamID, userID)` resolves with project_members winning, falling back to team_members. `canAdminProject` / `canEditProject` helpers gate every project + deployment write; reads stay any-role. New endpoints: `GET /list_members`, `POST /add_member`, `POST /update_member_role`, `POST /remove_member` under `/v1/projects/{id}/`. | `synapse/internal/db/migrations/000008_project_members`, `synapse/internal/api/projects.go::effectiveProjectRole`, `dashboard/components/ProjectMembersPanel.tsx` |
| **Auto-update from the dashboard (v1.1.0+)**: yellow banner in the `/teams` layout polls `GET /v1/admin/version_check` hourly (cached GitHub `/releases/latest` fetch, 15min TTL). One-click upgrade flow: confirm → `POST /v1/admin/upgrade` → modal polls `/upgrade/status` every 2.5s, streaming the daemon's log tail; on `synapse-api` restart (3 consecutive poll failures) flips to "page will reload in 90s" and force-reloads. Architecture: small Python 3 daemon on `/run/synapse/updater.sock` (unix socket, no TCP exposure), installed by `phase_install_updater` as a systemd unit. Lives outside docker compose so it survives the rebuild it triggers. `SYNAPSE_UPDATER_NO_RESTART=1` env flag (set by the daemon when forking setup.sh) keeps `phase_install_updater` from killing its parent mid-upgrade. Tier-1 honesty: revoke-style admin gating is "any user with team-admin role on at least one team". | `installer/updater/synapse-updater`, `installer/install/updater.sh`, `synapse/internal/api/admin.go`, `dashboard/components/UpdateBanner.tsx`, audit `upgradeStarted` |
| **Deploy keys (v1.0.3+)**: per-deployment named admin keys for CI integrations. `POST /v1/deployments/{name}/deploy_keys` mints a fresh admin key via `Docker.GenerateAdminKey` (signed by the deployment's INSTANCE_SECRET — backend accepts any signed key, so multiple keys per deployment coexist). Returns the full key + `.env` snippet + `export` snippet **once**; we store only `sha256` hash + 8-char prefix (GitHub-PAT model). `GET /v1/deployments/{name}/deploy_keys` lists active keys (non-revoked) with metadata only. `POST /v1/deployments/{name}/deploy_keys/{id}/revoke` flips `revoked_at`. **Tier-1 honesty**: revoke hides the row from the dashboard but does NOT invalidate the credential on the backend (admin keys are stateless there); to actually disable a leaked key, rotate the deployment. The dashboard surfaces this gotcha as a yellow banner in the panel. Migration 000009 repurposes the orphaned v0 `deploy_keys` table (renames `token_hash` → `admin_key_hash`, adds `admin_key_prefix` + `revoked_at`, partial unique on `(deployment_id, name) WHERE revoked_at IS NULL` so names are reusable after revoke). | `synapse/internal/api/deployments.go::createDeployKey/listDeployKeys/revokeDeployKey`, `synapse/internal/db/migrations/000009_deployment_deploy_keys`, `dashboard/components/DeployKeysPanel.tsx` |
| **Interactive installer wizard (v1.0.1)**: `curl \| bash` with no flags drops the operator into a 4-step Q&A walkthrough (Domain & TLS → Deployment mode → Install location → Dependencies) instead of failing on missing flags or assuming defaults. Reads from `/dev/tty` so it works under `curl \| bash` (stdin is the script, not the keyboard). Numbered menus (no arrow keys — cross-shell-portable). Auto-installs Docker via `get.docker.com` when missing + offers to install other host tools (`jq`, etc). "Not sure" branches auto-detect public IP, probe DNS for `--domain`, find free ports. Backwards-compatible: any pre-existing flag (`--domain=`, `--non-interactive`, `--no-tls`, `--enable-ha`, `--base-domain=`, `--install-dir=`) bypasses the wizard via `wizard::should_run`. Colour gate uses `[[ -r /dev/tty ]]` so colours render even when stdout is teed to the install log. 22 bats covering should_run gates + validators (305 → 327). | `installer/install/wizard.sh`, `setup.sh::phase_wizard`, `setup.sh::phase_autoinstall_docker` |
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
