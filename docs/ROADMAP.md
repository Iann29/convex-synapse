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
- [x] QUICKSTART verified end-to-end (register → team → project → real deployment provisioned in ~1s → cli_credentials snippet → adopt_deployment + bad-key path → delete cleans container+volume; adopted delete leaves Docker untouched). Re-verify on a truly fresh machine before tagging v1.

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
- [x] Migration helper: import an existing standalone self-hosted deployment into Synapse — `POST /v1/projects/{id}/adopt_deployment` with `/version` + `/api/check_admin_key` probe; `adopted=true` rows skip Docker.Destroy on delete and the health worker
- [x] Pagination on team / project listings — `?limit&?cursor` + `X-Next-Cursor` header on `/v1/teams`, `/v1/teams/{ref}/list_*`, `/v1/projects/{id}/list_deployments`

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

## v0.4 — "Looks the part" ✅ DONE

UI redesign to match the Convex Cloud dashboard aesthetic. Merged via
PR #1 on 2026-04-29.

- [x] Top app bar (team picker + tabs + profile menu)
- [x] Home page redesign (Projects / Deployments tabs, grid+list toggle, empty state)
- [x] Team Settings shell (left sidebar + General / Members / Access Tokens panes)
- [x] Avatar component with deterministic gradient + initials
- [x] Logo + favicon

## v0.5 — "HA-per-deployment" ✅ DONE

10/10 chunks merged. `ha:true` on `create_deployment` provisions 2 replicas backed by Postgres + S3 (AES-GCM-encrypted creds at rest); proxy fails over between replicas on connection error; health worker rolls up replica statuses into the deployment-level status; dashboard exposes a toggle + `HA ×N` badge. Single-replica deployments unchanged. Full design log: [docs/V0_5_PLAN.md](V0_5_PLAN.md). Operator guide: [docs/HA_TESTING.md](HA_TESTING.md).

- [x] **Chunk 1** — `internal/crypto/SecretBox` (AES-256-GCM envelope) for HA storage credentials encrypted at rest
- [x] **Chunk 2** — Postgres migrations 000004–000006: `deployment_storage` + `deployment_replicas` + `replica_id` on jobs + `upgrade_to_ha` job kind
- [x] **Chunk 3** — `dockerprov.Provision/Destroy/Restart Replica` (HA-aware container lifecycle alongside the legacy single-replica path)
- [x] **Chunk 4** — Health worker rolls up replica statuses to deployment status (any replica `running` → deployment `running`)
- [x] **Chunk 5** — Reverse proxy multi-replica picker + connection-error failover (`/d/{name}/*` retries on the next replica)
- [x] **Chunks 6 + 7** — Replica-aware provisioner worker + `create_deployment ha:true` happy path
- [x] **Chunk 8** — Dashboard HA toggle in the create-deployment dialog + `HA ×N` badge on deployment rows
- [x] **Chunk 9** — Gated real-backend HA e2e (`SYNAPSE_HA_E2E=1`) + `ha` compose profile (backend-postgres + minio)
- [x] **Chunk 10** — `POST /v1/deployments/{name}/upgrade_to_ha` endpoint with full validation (`ha_disabled` / `ha_misconfigured` / `already_ha` / `cannot_upgrade_adopted` / `deployment_not_running`); worker mechanics deferred to v0.5.1
- [x] Test counts: ~101 → ~131 Go (added crypto/ha provisioner/proxy/upgrade integration); 16 → 20 Playwright (HA toggle + badge specs)

## v0.6 — "Auto-installer" 🚀 IN PROGRESS

> **The installer is the single most important thing on the roadmap.** Synapse's reason to exist is to make self-hosting Convex painless. The current "clone the repo, edit .env, edit Caddyfile, sudo reload, docker compose up, manually verify" flow is the exact pain we're supposed to be solving. Operators should run **one command** and get a fully-configured production-ready install.

Full design + phased plan: **[docs/V0_6_INSTALLER_PLAN.md](V0_6_INSTALLER_PLAN.md)**.

North star:

```
$ curl -sf https://get.synapse.dev | sh
```

Two minutes later, the operator's VPS has Synapse running on `https://<their-domain>` with TLS, a registered admin user, and the Convex backend image pre-pulled.

- [ ] **v0.6.0 — Foundation.** `./setup.sh` script + supporting compose changes. 90% of single-VPS installs work end-to-end without manual file edits.
  - [ ] Chunk 1 — `installer/lib/detect.sh` + `port.sh` — pure-bash helpers + bats unit tests
  - [ ] Chunk 2 — `installer/install/preflight.sh` + `ui.sh` — colored pre-flight checks (OS / Docker / RAM / disk / DNS / outbound)
  - [ ] Chunk 3 — `installer/install/secrets.sh` + `env.tmpl` — idempotent secret generation, never overwrite existing values
  - [ ] Chunk 4 — `installer/install/caddy.sh` + templates — three-mode reverse-proxy detection (Caddy host / nginx / fresh)
  - [ ] Chunk 5 — `docker-compose.yml` `caddy` profile + standalone Caddyfile
  - [ ] Chunk 6 — `installer/install/compose.sh` + `verify.sh` — bring up the stack + self-test
  - [ ] Chunk 7 — `setup.sh` orchestrator with `main() { ... }; main "$@"` curl-pipe-shell truncation safety, ERR/EXIT traps, `flock` single-instance, `--non-interactive` / `--upgrade` / `--doctor` / `--uninstall` flags
  - [ ] Chunk 8 — Bats integration tests in Docker fixtures (debian-12 / ubuntu-24.04 / fedora-40)
  - [ ] Chunk 9 — README rewrite (Quickstart in 3 lines)
  - [ ] Chunk 10 — `docs/PRODUCTION.md` rewrite (manual flow demoted to appendix)
- [ ] **v0.6.1 — Lifecycle commands.** Same binary the installer drops, exposing maintenance subcommands.
  - [ ] `synapse status` — diagnostic snapshot (containers, ports, DNS, TLS, disk)
  - [ ] `synapse upgrade` — `git pull && docker compose pull && up -d` with rollback
  - [ ] `synapse backup` — `pg_dump` + per-deployment volume snapshots → tarball
  - [ ] `synapse restore <backup.tar>` — reverse of backup
  - [ ] `synapse uninstall` — remove everything, mandatory backup-first prompt
  - [ ] `synapse logs <component>` — aggregated logs (synapse / dashboard / postgres / caddy)
  - [ ] `synapse doctor` — runs the pre-flight checks against an existing install
- [ ] **v0.6.2 — Hosted install script.** `curl -sf https://get.synapse.dev | sh` one-liner pinned to git tags. (`get.synapse.dev` registration deferred — v0.6.2 may ship as a stable GitHub raw URL first.)
- [ ] **v0.6.3 — Browser-driven first-run wizard.** Dashboard's `/login` redirects to `/setup` when no users exist; walks the operator through admin-create → optional HA → demo deployment + CLI snippet. Operator never sees a config file.
- [ ] **v0.6.4 — Cloud images (stretch).** Pre-built DigitalOcean / Hetzner / Linode snapshots, Packer-built on each tag, listed in each provider's marketplace. Out of scope for the initial v0.6 milestone; bookmarked for v0.7.

## v0.5.1 — "HA polish" 📋 DEFERRED

Bookmarked but lower priority than v0.6. Both pieces are behind
already-shipped APIs (the wire surface exists — only the runtime
behind it needs to land), so adding them is a runtime-only change.

- [ ] Worker handler for `upgrade_to_ha` jobs: stream `snapshot_export`
  from the existing replica, provision 2 new HA replicas, run
  `snapshot_import` into the new pair, atomic swap (flip `ha_enabled`,
  mark old replica `stopped`, invalidate proxy cache, audit). Endpoint
  flips from `501` to `202` once the worker accepts the new `kind`.
- [ ] Real-backend failover e2e: extend `synapsetest.Setup` with an
  option to inject `*dockerprov.Client` instead of `FakeDocker`, then
  drive `docker kill` against the active replica from the
  `SYNAPSE_HA_E2E=1` test and assert traffic flows to the standby
  within 60s.
- [ ] Active health probe in `internal/proxy/`. Today
  `last_seen_active_at` is unset by anyone — the picker falls back to
  `replica_index ASC`. A 2s probe loop hitting `/api/check_admin_key`
  populates the column so the picker stabilises on the lease holder.

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
