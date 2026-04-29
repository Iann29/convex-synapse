# Roadmap

## v0.1 — "It runs end-to-end" ✅ DONE

Getting a fresh user from `git clone` to a running Convex backend container
provisioned via the dashboard.

- [x] Repo bootstrapped (git, README, structure)
- [x] Go backend boilerplate: chi, slog, /health
- [x] Postgres schema + migrations (embedded, applied at startup)
- [x] Auth: register, login, refresh, JWT middleware, /v1/me
- [x] Teams API: create, list, get, members, invites
- [x] Projects API: create, list, get, update, delete, env vars
- [x] Docker provisioner: ensures network/image, creates containers, allocates ports
- [x] Deployments API: create (with provisioning), list, get, delete, deploy keys, auth
- [x] Dashboard scaffold (Next.js + Tailwind)
- [x] docker-compose.yml: postgres + synapse + dashboard
- [x] Playwright e2e tests through the full compose stack (12 tests, ~21s)
- [x] Dashboard delete-deployment / delete-project / rename-project actions
- [x] Dashboard env-vars CRUD panel
- [x] Dashboard invites panel + /accept-invite page (multi-user e2e)
- [x] Dashboard skeleton loaders + copy-URL button + auto-refresh while provisioning
- [x] Backend invite list / cancel / accept (`POST /v1/team_invites/accept`)
- [x] CORS middleware
- [x] CI: Go build/vet/test + Next.js build + compose build + Playwright e2e
- [ ] QUICKSTART verified end-to-end on a fresh machine

## v0.2 — "It's nice" ✅ DONE

- [x] Personal access tokens (`POST /v1/create_personal_access_token`) + dashboard `/me`
- [x] Health monitoring worker — reconciles `deployments.status` with Docker reality every 30s
- [x] Real Go test suite (72+ test functions, ~7s, postgres testcontainer)
- [x] Async provisioning (returns 201 immediately; goroutine + 5min timeout + panic recovery + orphan-row sweep at startup)
- [x] Delete during provisioning is race-free (handler trusts the goroutine for cleanup)
- [x] `npx convex` CLI compatibility — admin keys now signed by Convex's `generate_key`; `cli_credentials` endpoint + dashboard panel
- [x] Reverse proxy mode so deployments don't need exposed host ports (`SYNAPSE_PROXY_ENABLED=true`)
- [x] Auto-restart for `stopped` deployments (`SYNAPSE_HEALTH_AUTO_RESTART=true`); missing-container is promoted to `failed`
- [x] Audit log: writer + `GET /v1/teams/{ref}/audit_log` + dashboard `/audit` page (admin-only)
- [x] Playwright e2e expanded to 16 tests (proxy mode, CLI credentials, multi-deploy, audit)
- [ ] Migration helper: import an existing standalone self-hosted deployment into Synapse
- [ ] Pagination on team / project listings (PAT list already paginated)

## v0.3 — "Multi-node hygiene" ✅ DONE

Three cheap changes that let you run N Synapse processes against the same
Postgres + Docker daemon without surprises. See
[`docs/DESIGN.md`](DESIGN.md) for the audit and trade-off discussion.

- [x] **Retry-on-conflict** for resource allocators (port, deployment name,
  team slug, project slug). UNIQUE-constraint races now retry transparently
  instead of surfacing 500s. Includes 30-goroutine race tests.
- [x] **Advisory locks** for periodic workers (health worker sweep, orphan
  provisioning sweep at startup). Single node behaves identically; multiple
  nodes coordinate so exactly one runs the work at any instant.
- [x] **Persistent provisioning queue** (`provisioning_jobs` table +
  `internal/provisioner.Worker`). Replaces the in-process goroutine.
  `SELECT FOR UPDATE SKIP LOCKED` shards across nodes and goroutines
  (default concurrency=4). Crashed workers auto-recover via `requeueStale`
  on the next Run.
- [x] Test counts: ~88 → ~101 Go (integration + new unit/race/advisorylock/provisioner); 16/16 Playwright in ~1.6 min.

## v0.4 — "Looks the part"

UI redesign to match the Convex Cloud dashboard aesthetic. Tracked in
[docs/DESIGN.md](DESIGN.md). Will be developed on a feature branch by a
frontend-specialised agent and merged via PR (not direct to main).

- [ ] Top app bar (team picker + tabs + profile menu)
- [ ] Home page redesign (Projects / Deployments tabs, grid+list toggle, empty state)
- [ ] Team Settings shell (left sidebar + General / Members / Access Tokens panes)
- [ ] Avatar component with deterministic gradient + initials
- [ ] Logo + favicon

## v0.5 — "HA-per-deployment"

The control plane is multi-node-safe (v0.3); the Convex backend itself is
single-writer per deployment by design (lease in `crates/postgres/src/lib.rs`
of `get-convex/convex-backend`). Active-passive failover is achievable on
top of upstream as-is — see [docs/DESIGN.md](DESIGN.md). This is the right
bet for moving the user-perceived reliability needle.

- [ ] Switch the provisioned Convex backend's persistence from SQLite to
  Postgres (existing flag `--db postgres`)
- [ ] S3-compatible blob storage for file/exports/snapshots
- [ ] Run 2 backend containers per deployment behind an HTTP load balancer
  with `/version` healthcheck
- [ ] Document the lease takeover characteristics (cold rebuild of in-memory
  indexes, seconds-not-zero failover)

## v1.0 — "Safe to depend on"

- [x] Audit log writer + reader (subset of cloud's vocabulary)
- [ ] Custom domains with auto-TLS
- [ ] Volume snapshot backups → S3
- [ ] RBAC: project-level roles
- [ ] OAuth/SSO via OIDC (works with Authentik, Zitadel, Keycloak)
- [ ] Kubernetes provisioner (alternative to Docker)
- [ ] Helm chart
- [ ] Public API stability guarantees + versioned releases

## Maybe never

- Full Stripe/Orb billing parity (irrelevant for self-hosted)
- LaunchDarkly equivalent (use static config + env vars)
- WorkOS-specific paths (use OIDC instead)
- Discord/Vercel/etc integrations (out of scope)

## Compatibility scorecard

OpenAPI v1 endpoint coverage today:

| Resource | Coverage |
|---|---|
| Auth | custom (no WorkOS) |
| Profile (`/me`) | ✅ |
| Teams | ~80% — no SSO, no billing endpoints |
| Projects | ~70% — no preview deploy keys, no transfer |
| Deployments | ~60% — no transfer, no custom domains, no patch |
| Personal access tokens | ✅ create / list / delete |
| Team invites | ✅ list / cancel / accept (custom: opaque-token URL flow) |
| Audit log | ✅ team-scoped read; admin-only |
| Reverse proxy | ✅ `/d/{name}/*` (custom — Cloud has dedicated subdomains) |
| CLI compat | ✅ `cli_credentials` endpoint + signed admin keys |
| Cloud backups | ❌ v1.0 |

The dashboard fork (when complete) covers data, functions, logs, schedules,
files, history, and per-deployment settings — all by talking directly to the
Convex backend with the admin key Synapse hands out, no extra work.
