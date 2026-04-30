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

The control plane is multi-node-safe (v0.3); the Convex backend itself is
single-writer per deployment by design (lease in `crates/postgres/src/lib.rs`
of `get-convex/convex-backend`). Active-passive failover is achievable on
top of upstream as-is — see [docs/V0_5_PLAN.md](V0_5_PLAN.md) for the
detailed scoping. This is the right bet for moving the user-perceived
reliability needle.

Chunks landed:
- [x] **Chunk 1** — Schema + models + AES-GCM secrets helper. Migration
  000004 adds `deployment_storage` + `deployment_replicas` tables and
  `deployments.ha_enabled` / `replica_count` columns; backfills one
  replica row per existing deployment. `internal/crypto/secrets.go`
  envelopes connection material with `SYNAPSE_STORAGE_KEY`. (commit
  `8fbba60`)
- [x] **Chunk 2** — Replica-aware Docker provisioner. `DeploymentSpec`
  grows `ReplicaIndex`, `HAReplica`, `Storage` (Postgres + S3 env vars).
  `ContainerName` / `VolumeName` keep the legacy single-replica naming
  unchanged; HA replicas pick up the `-{idx}` suffix. New
  `DestroyReplica` / `RestartReplica` / `StatusReplica` methods.
  (commit `421bd4a`)
- [x] **Chunk 3** — Cluster config + `ha:true` request gate.
  `SYNAPSE_HA_ENABLED` + `SYNAPSE_BACKEND_*` envs propagate to the
  handler; `create_deployment` validates and refuses with
  `ha_disabled` / `ha_misconfigured`. (commit `c9f04cb`)
- [x] **Chunk 4** — Replica-aware health worker. Iterates
  `deployment_replicas` rows, rolls up replica statuses into the
  deployment-level status, calls `Restart` / `RestartReplica` based on
  HA mode. (commit `47614a3`)
- [x] **Chunk 5** — Replica-aware proxy resolver + failover. `ResolveAll`
  returns the ordered replica list (`last_seen_active_at DESC`); proxy
  retries down the slice on connection-level errors. New
  `ErrNoReplicas` → 503 distinct from 404. (PR #2, commit on `main`)
- [x] **Chunks 6+7** — `ha:true` provisions for real. `provisioner.Worker`
  reads replica jobs, decrypts `deployment_storage`, calls Provision
  with HA spec; `create_deployment` allocates 2 ports, writes the
  storage row encrypted, inserts 2 replica rows + 2 jobs in one tx;
  the replica-aware pre-check stops siblings from skipping each other.
  (PR #3, commit on `main`)
- [x] **Chunk 8** — Dashboard HA toggle + badge. "High availability (2
  replicas + Postgres + S3)" checkbox in the create-deployment dialog;
  `HA ×N` badge on deployment rows. Backend errors surface inline.
  (PR #4, merged)
- [x] **Chunk 9** — Gated real-backend e2e + `ha` compose profile.
  `docker compose --profile ha up` brings up backend-postgres + minio;
  `synapse/internal/test/ha_real_e2e_test.go` is gated by
  `SYNAPSE_HA_E2E=1`. Operator setup walkthrough lives in
  [docs/HA_TESTING.md](HA_TESTING.md). (PR #6, merged)
- [x] **Chunk 10** — `POST /v1/deployments/{name}/upgrade_to_ha`
  endpoint with full validation (`ha_disabled`, `ha_misconfigured`,
  `already_ha`, `cannot_upgrade_adopted`, `deployment_not_running`)
  and audit-event recording. Worker mechanics (snapshot_export →
  re-provision → snapshot_import → swap) deferred to v0.5.1; today
  the happy path returns `501 ha_upgrade_not_yet_implemented` with a
  pointer to V0_5_PLAN.md. (PR #7, merged)

## v0.5.1 — "HA polish" 📋 NEXT

The mechanical pieces that didn't fit in v0.5's main slice. Both are
behind already-shipped APIs, so adding them is a runtime-only change.

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
