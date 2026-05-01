# AGENTS.md

Conventions for AI coding agents (Claude Code, Codex, Cursor, Aider, etc.)
contributing to this repository. The goal is a consistent commit history and
a low-friction onboarding for the next agent that picks up the work.

For Claude Code-specific guidance see [CLAUDE.md](CLAUDE.md). This file is
the cross-tool subset.

## Ground rules

1. **Build green before commit.** `cd synapse && go build ./... && go vet ./... && go test ./... -count=1`
   must succeed. Playwright suite (`cd dashboard && npx playwright test`) too
   if you touched anything dashboard-side or backend-handler-side.
2. **One feature per commit.** Mixing a refactor with a bug fix makes the
   history less useful. Follow the conventional-commit scopes in `git log`.
3. **End-to-end test each slice.** New endpoint? Add a `synapse/internal/test/<resource>_test.go`
   case. New UI flow? Add a `dashboard/tests/<feature>.spec.ts`. Don't ship
   "tested manually with curl" for anything user-visible.
4. **Write the WHY in comments.** "What" is in the code; "why" decays into
   tribal knowledge if it isn't pinned next to the line that depends on it.
5. **Never widen scope silently.** If a small task forces a refactor, do
   the refactor in its own commit first.

## Repo orientation

```
synapse/                Go backend (chi, pgx, docker SDK)
  internal/api/         HTTP handlers
  internal/audit/       Best-effort audit-log writer
  internal/auth/        JWT + bcrypt + opaque PAT helpers
  internal/crypto/      AES-GCM SecretBox for HA storage secrets (v0.5+)
  internal/db/          pgx pool, migrations, retry/advisory-lock helpers
  internal/docker/      Docker SDK + Provision/Destroy/Restart (single + replica variants)
  internal/health/      Periodic reconciler — replica-aware aggregate roll-up
  internal/middleware/  chi middleware (auth, logging, CORS)
  internal/provisioner/ Persistent job queue + parallel worker (HA-aware claim path)
  internal/proxy/       /d/{name}/* reverse proxy with multi-replica failover
  internal/test/        Integration test harness (Setup + SetupHA), package synapsetest

dashboard/              Next.js 16 + Tailwind 4 + Playwright
docs/                   Architecture, roadmap, design, quickstart, API,
                        V0_5_PLAN.md (HA scoping), HA_TESTING.md (operator setup)
docker-compose.yml      Local stack + optional `ha` profile (backend-postgres + minio)
```

Read `docs/ARCHITECTURE.md`, `docs/DESIGN.md`, and (for HA work)
`docs/V0_5_PLAN.md` end-to-end before making non-trivial changes — the
trade-offs are explained there and not always re-stated in the code.

## Multi-node ground rules

The codebase is meant to run with N processes against one Postgres + one
Docker daemon. Three patterns to follow when you write new code:

- **Resource allocators** (anything that does "SELECT to find a free X, then
  INSERT it"): wrap in `db.WithRetryOnUniqueViolation`. UNIQUE catches the
  race; retry generates a fresh candidate. v0.5: `allocatePorts(N)` is the
  multi-port variant; UNIQUE constraints live on both
  `deployments.host_port` (legacy) and `deployment_replicas.host_port`.
- **Periodic workers**: wrap each tick in `db.WithTryAdvisoryLock(pool, key, fn)`.
  Single-node always acquires; multi-node coordinates.
- **Long async work**: enqueue a row, run a worker with
  `SELECT FOR UPDATE SKIP LOCKED` + parallel goroutines. Don't spawn a
  per-handler goroutine.

See `internal/provisioner/` for the canonical example of all three.

## HA ground rules (v0.5+)

HA-per-deployment is opt-in via `SYNAPSE_HA_ENABLED=true` +
`SYNAPSE_STORAGE_KEY=<32 bytes hex>`. Single-replica deployments are
unaffected — but every NEW code path should:

- **Read replicas, not legacy columns.** `deployment_replicas` is the
  source of truth for `host_port` / `container_id` / replica `status`.
  `deployments.host_port` is kept populated for back-compat with v0.4
  callers; new readers go through the replica join.
- **Aggregate up to deployment status.** A deployment is `running` if
  any replica is `running`; `failed` only when all are
  `failed`/`stopped`. See `health.Worker.recomputeDeploymentStatus`.
- **Never log decrypted secrets.** `crypto.SecretBox.DecryptString`
  returns plaintext that goes into a container's env vars and nowhere
  else. No `slog.Info("worker: provisioning", "url", postgresURL)`.
- **Single-replica is HAReplica=false, ReplicaIndex=0.** The naming
  helpers (`docker.ContainerName`, `docker.VolumeName`) keep legacy
  names unchanged for non-HA — don't break existing operator scripts
  that filter `convex-{name}` containers.

## v0.6 ground rules (installer)

The auto-installer (`setup.sh` + `installer/`) is pure bash. The
`synapse-installer` skill captures the bash conventions in detail.
Cross-reference rules:

- **Real-VPS validation** is part of "done" for installer/compose
  changes. The bats suite runs ~211 unit tests but doesn't see real
  Docker, real Caddy, real DNS, or a real Next.js build. Chunk 7 of
  v0.6.0 (PR #19) caught 6 bugs in a single real-VPS run that ALL
  had green bats CI. Subsequent fix-ups (PR #23/#24/#25) added
  three more. The lesson: smoke against `synapse-vps` after any
  setup.sh / docker-compose.yml / handler-URL change.
- **`[[ ]] && cmd` is a set-e footgun** when it's the last expression
  in a function. Use explicit `if`/`fi` for top-level conditionals.
- **`NEXT_PUBLIC_*` is build-time** in Next.js — pass via
  `build.args` in docker-compose, not just `environment:`.
- **API responses are camelCase** (Convex Cloud OpenAPI shape) —
  `accessToken`, `projectId`, `convexUrl`, NOT `access_token`.
- **`docker compose pull` fails on services with `build:`** — use
  `up -d --build` instead.
- **The Convex backend image must be pre-pulled** before the first
  `create_deployment` — Synapse calls `docker run` against it
  directly and 500s with "no such image" otherwise.

## URL rewrite contract (PR #10 + #24)

Every endpoint that returns a `models.Deployment` (raw or wrapped)
MUST apply `publicDeploymentURL(&d)` before `writeJSON`. Currently
six handlers do (createDeployment, adoptDeployment, getDeployment,
getProjectDeployment, two listDeployments). New handlers that
return a deployment shape must follow the same pattern — otherwise
remote callers get the loopback URL the provisioner stored.

`TeamsHandler` and `ProjectsHandler` carry a `Deployments
*DeploymentsHandler` field for this; new handlers in other files
should too.

## What "done" looks like

A feature is done when:

- [ ] `cd synapse && go build ./... && go vet ./... && go test ./... -count=1` is clean
- [ ] The endpoint is reachable on a freshly-truncated DB
- [ ] Auth/authz checks (member-only, admin-only) are exercised
- [ ] Errors return structured JSON via `writeError(...)` with stable codes
- [ ] **Integration test** added in `synapse/internal/test/<resource>_test.go`
- [ ] **Playwright spec** added in `dashboard/tests/` if user-facing
- [ ] **Audit hook** added (`audit.Record(...)`) on every mutating success path
- [ ] **Bats test** added in `installer/test/` if you touched `setup.sh` / `installer/`
- [ ] **Real-VPS smoke test** passes if you touched setup.sh, docker-compose.yml, or any handler that emits a URL — `ssh synapse-vps` and run end-to-end
- [ ] `docs/API.md` updated for any new/changed endpoint
- [ ] If you added a deployment-returning endpoint: applied `publicDeploymentURL(&d)` rewrite (otherwise remote callers see loopback URLs)
- [ ] Commit message body lists the curl flow you actually ran
- [ ] `docs/ROADMAP.md` ticked if you crossed a phase boundary

## What's intentionally out of scope

We aim for ~80% of Convex Cloud's stable v1 OpenAPI surface. We will NOT
implement (or only as much as the user explicitly asks for):

- Stripe/Orb billing parity
- WorkOS/SAML (we have email+password JWT; OIDC is v1.0+)
- Multi-region / deployment classes
- Discord/Vercel integrations
- LaunchDarkly equivalents

When in doubt: simpler beats complete.
