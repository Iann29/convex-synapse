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

## v0.6 — "Auto-installer" ✅ DONE

> **The installer is the single most important thing on the roadmap.** Synapse's reason to exist is to make self-hosting Convex painless. v0.6 ships every piece: foundation + lifecycle commands + `curl | sh` one-liner + browser first-run wizard. Tagged as `v0.6.3` on GitHub Releases.

Full design + phased plan: **[docs/V0_6_INSTALLER_PLAN.md](V0_6_INSTALLER_PLAN.md)**.

North star (achieved):

```
$ curl -sSf https://raw.githubusercontent.com/Iann29/convex-synapse/main/setup.sh \
    | bash -s -- --domain=synapse.example.com
```

Three minutes later, the operator's VPS has Synapse running on `https://<their-domain>` with TLS, the admin user created via the browser wizard, and the Convex backend image pre-pulled.

- [x] **v0.6.0 — Foundation ✅ DONE.** `./setup.sh` script + supporting compose changes. **Validated end-to-end against a real Hetzner CPX22** (Ubuntu 24.04). One-line `git clone && ./setup.sh --domain=<host>` produces a working install in ~3 min cold.
  - [x] Chunk 1 — `installer/lib/detect.sh` + `port.sh` — pure-bash helpers + 66 bats unit tests (PR #12; CRLF, Mint codename, `df -kP`, host-deps fixes after independent code-review)
  - [x] Chunk 2 — `installer/install/preflight.sh` + `ui.sh` — colored pre-flight checks (OS / arch / sudo / Docker / Compose / RAM / disk / outbound / DNS) (PR #13)
  - [x] Chunk 3 — `installer/install/secrets.sh` + `env.tmpl` — idempotent secret generation (Coolify `update_env_var` pattern; never overwrites existing values) (PR #14, header-comment fix in #17)
  - [x] Chunk 4 — `installer/install/caddy.sh` + templates — three-mode reverse-proxy detection (Caddy host / nginx / fresh) with managed-block upsert (BEGIN/END markers, idempotent) (PR #15)
  - [x] Chunk 5 — `docker-compose.yml` `caddy` profile + standalone Caddyfile (PR #16)
  - [x] Chunk 6 — `installer/install/compose.sh` + `verify.sh` — bring up the stack + post-install self-test (register → team → project → deployment → assert public URL) (PR #18)
  - [x] Chunk 7 — `setup.sh` orchestrator with `main() { ... }; main "$@"` curl-pipe-shell truncation safety, ERR/EXIT traps, `flock` single-instance, full CLI flag surface. **6 real-world bugs found + fixed during real-VPS validation** (set-e footguns, `compose pull` on `build:` services, missing `jq`/`dig`, camelCase response shapes, backend image pre-pull, loopback URL on `--no-tls`) (PR #19)
  - [x] Chunk 8 — `setup.sh` smoke tests (15 cases): `--version` / `--help` / unknown-flag / `parse_flags` branches / `bash -n` syntax check on every shipped `.sh`. Container-fixture integration tests bookmarked for v0.6.1+ (real-VPS validation already proves end-to-end) (PR #20)
  - [x] Chunk 9 — README rewrite: Quickstart in 3 lines via `./setup.sh` (PR #21)
  - [x] Chunk 10 — `docs/PRODUCTION.md` rewrite: leads with `setup.sh`, manual flow demoted to "Appendix: manual install (advanced)" (PR #22)
  - [x] **Fix-up #23** — public-IP fallback when `--no-tls` + no `--domain`: `setup.sh` calls `detect::public_ip` (api.ipify → ifconfig fallback) so dashboard JS in a remote browser hits `http://<vps-ip>:8080` instead of `localhost:8080`. Plus `docker-compose.yml` dashboard service gains `build.args.NEXT_PUBLIC_SYNAPSE_URL` because Next.js bakes the var at build time, not runtime
  - [x] **Fix-up #24** — `publicDeploymentURL` rewrite extended to all 6 deployment-emitting handlers (createDeployment, adoptDeployment, getDeployment, getProjectDeployment, both listDeployments). PR #10 only covered `/auth` + `/cli_credentials`; the dashboard reads from the GET endpoints, so it was rendering loopback URLs until this. 5 new Go integration tests pin the contract
  - [x] **Fix-up #25** — `docker-compose.yml` synapse service now passes `SYNAPSE_PUBLIC_URL` + `SYNAPSE_ALLOWED_ORIGINS` from the `.env` into the container. Without this the value lived in `.env` but never reached the binary, so `h.PublicURL` was empty and the rewrite was a no-op even after #24
  - [x] **Fix-up #26** — Convex Dashboard hosted alongside Synapse (the data/functions/logs UI for individual deployments), auto-logged-in via `postMessage` handshake. New `/embed/<name>` route in the dashboard fork iframes the upstream `ghcr.io/get-convex/convex-dashboard` image and replies to its `dashboard-credentials-request` postMessage with the deployment's adminKey + URL. A Caddy sidecar in front of the convex-dashboard container strips its `X-Frame-Options` + `frame-ancestors` headers so the iframe can render. This was originally bookmarked for v0.6.3 but it's UX-critical (without it operators can't see their data), so brought forward
  - [x] Test counts after v0.6.0 + fix-ups: 211 bats + 136 Go (+5 URL-rewrite integration tests); shellcheck `-x` clean across 9 .sh files
- [x] **v0.6.1 — Lifecycle commands ✅ DONE.** `setup.sh` exposes maintenance subcommands; same script the installer drops in `$INSTALL_DIR`. All four chunks merged + real-VPS validated end-to-end on `synapse-vps` (Hetzner CPX22).
  - [x] `setup.sh --doctor` — preflight checks against an existing install (already shipped with v0.6.0)
  - [x] **Chunk 1** — `setup.sh --upgrade [--ref=<git-ref>] [--force]`: clones target ref → rsync into install dir (preserves .env / Caddyfile / log / snapshot) → pre-pull external images → `compose up -d --build` → wait `/health` → on failure, re-tag from `.upgrade-snapshot.tsv` and bring stack back up. Auto-detects target via GitHub Releases /latest. SYNAPSE_VERSION stamped in .env (slashes sanitized → `-`). Audit log at `$INSTALL_DIR/upgrade.log`. Real-VPS validated v0.6.0 → feat-branch and idempotent re-runs (PR #27)
  - [x] **Chunk 2** — `setup.sh --backup [--out=<path>] [--exclude-env]` + `setup.sh --restore=<archive> [--keep-env] [--non-interactive]`: tarball with manifest.txt + .env + docker-compose.yml + pg_dump + per-deployment volume tarballs. Restore wipes pgdata + per-deployment volumes, replays the dump, brings stack up. Real-VPS validated 10-volume install end-to-end (PR #28)
  - [x] **Chunk 3** — `setup.sh --uninstall [--skip-backup] [--keep-volumes] [--non-interactive]`: takes a backup first by default, wipes volumes (a volume without its matching .env can't be reused — the backup is the canonical recovery via re-install + --restore). `--keep-volumes` is a power-user opt-out for operators who saved .env outside the install dir. Strips host-Caddy managed block on caddy_host installs. Real-VPS validated full uninstall → reinstall → restore loop (PR #29)
  - [x] **Chunk 4** — `setup.sh --logs=<component> [--follow] [--tail=<n>]` + `setup.sh --status`: thin pass-through to `docker compose logs` for one service (strict component validation) + read-only diagnostic snapshot (containers, volumes, DNS, TLS expiry, disk). Real-VPS validated (PR #31)
  - [x] Test counts after v0.6.1: 211 → 258 bats (47 new lifecycle/uninstall/logs/status cases) + 3 new secrets bats; shellcheck `-x` clean across 11 .sh files
- [x] **v0.6.2 — Hosted `curl | sh` ✅ DONE.** `curl -sSf https://raw.githubusercontent.com/Iann29/convex-synapse/main/setup.sh | bash -s -- --domain=...`. `setup::needs_bootstrap` detects the `installer/`-not-on-disk case (BASH_SOURCE[0] empty under `curl | bash`); `setup::bootstrap` clones into `/tmp/convex-synapse-bootstrap-<pid>` and exec's the cloned setup.sh. `--no-bootstrap` opts out for tests; `SYNAPSE_BOOTSTRAP_REF` env pins the ref. Real-VPS validated end-to-end. (`get.synapse.dev` vanity domain still deferred — raw URL is the canonical install path until tags are cut.) (PR #32)
- [x] **v0.6.3 — Browser-driven first-run wizard ✅ DONE.** Dashboard `/login` probes `/v1/install_status` and redirects to `/setup` when `firstRun=true`. The wizard walks the operator through admin-create → demo team / project / dev deployment → land on the project page with the deployment row visible (CLI snippet one click away). Skip-demo lands on `/teams` empty state for advanced flows. New backend endpoint: `GET /v1/install_status` (public, no auth, EXISTS check on users table). `setup.sh::phase_verify` now `TRUNCATE users CASCADE` after self-test (FK ON DELETE RESTRICT on `teams.creator_user_id` blocks row-level user delete; CASCADE follows the FK tree) so a fresh install lands at zero-user state and the wizard fires. 3 Go integration tests + 4 Playwright cases. HA toggle in the wizard intentionally deferred to v0.7+ (operator path requires cluster Postgres + S3 already configured). (PR #33 + fix-up #34)
- [x] Test counts after the full v0.6.x: 211 → 266 bats + 136 → 139 Go + 20 → 24 Playwright; shellcheck `-x` clean across 11 .sh files. All real-VPS validated end-to-end on `synapse-vps` (Hetzner CPX22).
- ~~v0.6.4 — Cloud images (stretch)~~ — deprioritized 2026-05-01; bookmarked for v0.7+ if it surfaces as a real operator ask.

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

## v1.0 — "Safe to depend on" 🚀 IN PROGRESS

The v1.0 surface area takes Synapse from "works for one operator on a Hetzner box" to "ships to thousands of self-hosters across providers". Each item below is its own chunk-able body of work — operator picks priority.

### ✅ Shipped this milestone

- [x] **Audit log** writer + reader (subset of cloud's vocabulary)
- [x] **Custom domains with auto-TLS** — `SYNAPSE_BASE_DOMAIN=<host>` makes deployment URLs `https://<name>.<host>` instead of `<host>/d/<name>/`. Caddy on-demand TLS issues per-host certs; `/v1/internal/tls_ask` gates issuance on real, non-deleted deployments. Real-VPS smoke pending wildcard DNS setup (operator-side).
  - [x] Chunk 1 — `SYNAPSE_BASE_DOMAIN` config, `publicDeploymentURL` rewrite, proxy Host-header routing, `/v1/internal/tls_ask` endpoint. 14 new Go tests (139 → 146) (PR #35)
  - [x] Chunk 2 — `setup.sh --base-domain=<host>`, env.tmpl, DNS preflight (`check_base_domain` synthetic-subdomain probe), Caddy global `on_demand_tls { ask }`, new `caddy.wildcard` template appended to standalone + host fragments. 7 new bats (266 → 273) (PR #36)

### 📋 Left to ship (priority order — operator can override)

Effort scale: **S** ≈ 1 session · **M** ≈ 2-3 sessions · **L** ≈ multi-week.

- [ ] **Custom domains chunk 3 — dashboard polish (S)**. Deployment row hover explains the new URL form; success screen mentions custom-domain mode; operator-facing copy in `--status` reflects the wildcard. No new mechanics.

- [ ] **Volume snapshot backups → S3 (M)**. Extension of v0.6.1 chunk 2 (`setup.sh --backup`). Same archive format, but write to a configured S3 bucket on a cron schedule. Retention policy. Operator opts in via env vars (`SYNAPSE_BACKUP_S3_BUCKET`, `SYNAPSE_BACKUP_SCHEDULE`). Touches: `lifecycle.sh` (s3-aware path), new env vars, audit log entries. Solves: "I lost my VPS, I want my backups in S3."

- [ ] **RBAC: project-level roles (M)**. Today roles are team-scoped (admin / member). Add admin / member / viewer per project so a contractor on team can edit project A but only view project B. Touches: db migration adding `project_members` table, every project handler's authz check, dashboard role-toggle UI in the Members pane. Solves: "I can't safely invite my team without per-project gates."

- [ ] **OAuth / SSO via OIDC (M-L)**. Works with Authentik, Zitadel, Keycloak, Google Workspace, Okta. Auth handler grows an OIDC discovery + callback flow alongside email+password. JWT issuer accepts the OIDC sub claim. Dashboard `/login` adds "Sign in with `<Provider>`" when configured. Touches: `internal/auth/`, `internal/api/auth.go`, dashboard auth, env vars (`SYNAPSE_OIDC_ISSUER`, `SYNAPSE_OIDC_CLIENT_ID`, etc). Solves: "Company won't let me ship without SSO."

- [ ] **Public API stability guarantees + versioned releases (S)**. Already half-shipped — `v0.6.3` tagged on GitHub Releases, `--upgrade` queries the API. Outstanding: semver discipline on the OpenAPI shape (breaking changes bump major), document the contract in `docs/API.md`, add a deprecation policy. Touches: docs only.

- [ ] **Kubernetes provisioner (L)**. Alternative to Docker. The `Provisioner` interface is already factored (`Provision/Destroy/Restart`). Add `internal/k8sprov/` that creates Deployment + Service + PVC per Synapse deployment. Configured via `SYNAPSE_PROVISIONER=k8s` + kubeconfig. Touches: `cmd/server/main.go` wiring, `internal/health/` (k8s-aware status), Helm chart (depends-on). Solves: "I run K8s, can't introduce Docker."

- [ ] **Helm chart (L)**. Installs Synapse on an existing K8s cluster. Helm umbrella over postgres (CloudNative-PG operator), synapse, dashboard, optional cluster-issuer for cert-manager. Depends on the Kubernetes provisioner above. Touches: new `helm/` dir, GitHub Actions to publish to a chart repo on each tag.

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
| Deployments | ~70% — custom domains ✅ (v1.0); no transfer, no patch |
| Personal access tokens | ✅ create / list / delete |
| Team invites | ✅ list / cancel / accept (custom: opaque-token URL flow) |
| Audit log | ✅ team-scoped read; admin-only |
| Reverse proxy | ✅ `/d/{name}/*` (custom — Cloud has dedicated subdomains) |
| CLI compat | ✅ `cli_credentials` endpoint + signed admin keys |
| Cloud backups | ❌ v1.0 |

The dashboard fork (when complete) covers data, functions, logs, schedules,
files, history, and per-deployment settings — all by talking directly to the
Convex backend with the admin key Synapse hands out, no extra work.
