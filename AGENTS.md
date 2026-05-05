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
  changes. The bats suite runs 266 unit tests but doesn't see real
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

## v0.6.1 ground rules (lifecycle commands)

`setup.sh` is the entry point for everything an operator does after
the initial install — `--upgrade`, `--backup`, `--restore`,
`--uninstall`, `--logs`, `--status`, `--doctor`. They live in
`installer/install/lifecycle.sh` (one function per command) and are
wired via the early-return branches in `setup.sh::main()`.

- **Validate first, mutate second.** Every lifecycle command starts
  by asserting `$INSTALL_DIR/.env` and `docker-compose.yml` exist.
  No silent "create on the fly" — the operator wanted to mutate an
  install they expected to be there.
- **Audit trail to `$INSTALL_DIR/upgrade.log`** (or `backup.log`,
  etc). ISO-8601 timestamps. Best-effort — never fails the user
  request. This is the first thing operators `tail` when something
  goes sideways.
- **Cleanup pattern: NO `trap RETURN`.** Bash fires it on every
  function return inside the trap-setting function (ui::spin,
  helpers, ...) — you'll wipe your work-dir before rsync ever runs.
  Wrap the command logic in `_<cmd>_inner` and have the public
  function rm/cleanup once on return, with state passed back via
  `printf -v` + a non-shadowing var name. See `lifecycle::upgrade`.
- **Image tags ≠ version stamps.** `docker-compose.yml` pins
  `synapse:local` and `synapse-dashboard:local` regardless of
  `SYNAPSE_VERSION` because the version may contain characters
  Docker rejects in tags (`feat/foo`). Snapshot/rollback uses
  image_id, not the tag — so `:local` is robust. Lifecycle commands
  that stamp a version into .env should also sanitize `/` → `-`
  belt-and-suspenders for any legacy compose still using
  `${SYNAPSE_VERSION}` as a tag.
- **`docker compose images --format json` uses `.ContainerName`,
  NOT `.Service`.** Older docs/examples have it wrong.
- **Match volumes by suffix, not predicted project name.** Compose's
  project-name resolution depends on `COMPOSE_PROJECT_NAME`, the
  parent dir of the compose file, and operator overrides — no helper
  in shell can predict the volume name reliably. Iterate
  `docker volume ls -q | grep 'synapse-pgdata$'` (or `synapse-data-`)
  instead. Real-VPS smoke caught a `synapse_synapse-pgdata` even
  though the install dir was `/opt/synapse-test`.
- **`bash -c "... | psql >/dev/null 2>&1"` swallows psql's exit code.**
  Without `set -o pipefail` (which doesn't auto-inherit into
  `bash -c`) the rc is whichever finished last. For pg_dump pipelines,
  set pipefail explicitly inside the bash -c. For psql replay, decompress
  to a sibling `.sql` file and use `< file` redirect — no pipe needed.
- **`pg_isready` returns 0 during postgres's first-init reboot cycle.**
  The container boots, creates the user, SHUTS DOWN, then restarts.
  `pg_isready` passes during the FIRST boot but connections during
  the shutdown window fail with "the database system is shutting down".
  Use a `psql -tAc 'SELECT 1'` retry loop (90s budget) instead.

## v0.6.2 ground rules (curl | sh bootstrap)

`setup.sh` is meant to work both as a local `./setup.sh` and as a
hosted `curl -sSf https://raw.githubusercontent.com/.../main/setup.sh
| bash -s -- ...` one-liner. Under the latter, only `setup.sh` is
in the bash process — `installer/` lives nowhere on disk.

- **`HERE` defaults to empty string** when `BASH_SOURCE[0]` is
  unset/empty (the `curl | bash` case). `set -u` would NPE if we
  unconditionally `cd $(dirname "$BASH_SOURCE")` — guard with the
  `_setup_src` resolver before `readonly INSTALLER_TEMPLATES`.
- **Bootstrap re-exec runs BEFORE source_libs.** `setup::needs_bootstrap`
  returns true when `$HERE` is empty OR `$HERE/installer` is missing.
  `setup::bootstrap` clones to `/tmp/convex-synapse-bootstrap-$$`
  (or `$HOME/.synapse-bootstrap-$$` fallback), exports
  `SYNAPSE_BOOTSTRAPPED=1`, and `exec`s the cloned setup.sh with `"$@"`.
- **`--no-bootstrap` flag** disables the re-exec for tests and for
  operators running setup.sh from a custom checkout that's missing
  `installer/` for some reason.
- **`SYNAPSE_BOOTSTRAP_REF` env** pins the git ref the bootstrap
  clones (default `main`). Future tagged releases can be reached via
  raw URL: `.../v0.7.0/setup.sh` (not yet — no tags cut).

## v0.6.3 ground rules (first-run wizard + install_status)

The `/v1/install_status` endpoint is the public, unauthenticated
probe the dashboard uses pre-login to decide between `/login` and
`/setup`. Treat it as a stable contract:

- **Public, no auth.** Mounted in the public group of `router.go`,
  next to `/auth`. The dashboard hits it before any JWT/PAT exists —
  there's no other surface that could carry the signal.
- **`firstRun = NOT EXISTS(SELECT 1 FROM users)`.** Cheap (postgres
  short-circuits at the first row). Don't add OR-clauses for
  "no teams" or "no projects" — the wizard's contract is "no humans
  have logged in yet", not "no infrastructure has been touched".
- **Fail closed.** A DB-unreachable probe returns 503, not
  `firstRun=false`. Misleading "everything's installed" sends the
  operator to `/login` of nothing — the 503 falls through to the
  normal login form (which itself fails clearly).
- **`setup.sh::phase_verify` MUST leave the DB at zero-user state**
  on success without `--keep-demo`. `TRUNCATE users CASCADE` is the
  surgical fix because `teams.creator_user_id ON DELETE RESTRICT`
  blocks any row-level user delete (FK constraint). The Convex API
  uses `POST /<resource>/delete` (not HTTP `DELETE`) — verify::_curl
  with `-X DELETE` 4xx's silently because `curl -f` is on.

## v1.0 ground rules (project-level RBAC)

Roles compose: `project_members.role` (override) wins over
`team_members.role` (fallback). Three roles at the project grain
(`admin` / `member` / `viewer`); `viewer` is project-only — there's
no team-level viewer.

- **Always go through `effectiveProjectRole(ctx, db, projectID, teamID, userID)`.**
  Don't `SELECT role FROM team_members ...` from a new handler — the
  helper does the COALESCE for you and stays correct when overrides
  exist. The two `load*ForRequest` helpers (project + deployment)
  already use it; mirror that pattern in new resource loaders.
- **Gate writes via `canAdminProject` / `canEditProject` (in projects.go).**
  Don't compare against a literal `models.RoleAdmin` — it locks out
  members from edits they're allowed to make and falls apart the
  moment a fourth role lands.
- **Permission matrix is the contract.** Reads (GET project /
  deployments / env vars / members) — any role. Edits (env vars,
  create deployment) — admin OR member. Destructive (delete
  deployment, delete/transfer project, adopt, upgrade_to_ha,
  create deploy key, manage members, project tokens) — admin only.
  See `docs/API.md` "Project-level RBAC" for the full table.
- **Team is the trust boundary.** `add_member` and
  `update_member_role` refuse 400 `not_team_member` when the
  target isn't on the project's owning team. To onboard someone
  brand-new to a project, invite them to the team first, then drop
  the override.
- **CASCADE goes from team → project_members.** A team_members row
  going away (user removed from team / user deleted / team deleted)
  takes their project_members rows with it. No orphan overrides.
- **last_admin guard NOT enforced on project_members.** Team admins
  always retain admin access via fallback unless they themselves
  carry a degrading override; demoting the last project_members
  admin row is allowed. If you find a real "operator locked themself
  out" case in the wild, add the guard then.

## v1.0 ground rules (custom domains)

`SYNAPSE_BASE_DOMAIN=<host>` enables per-deployment subdomains
(`<name>.<host>` instead of the path-based `<host>/d/<name>/`).
Caddy on-demand TLS issues per-host certs lazily; the
`/v1/internal/tls_ask` endpoint gates issuance on real, non-deleted
deployments so attackers can't burn Let's Encrypt quota.

- **Every new `SYNAPSE_*` env var needs THREE wire-up points** (see PR #25 + #38):
  1. `installer/templates/env.tmpl` — operator-facing, what they see in `.env`
  2. `docker-compose.yml` synapse service `environment:` block — container-facing, what reaches the binary
  3. `synapse/internal/config/config.go` — Go-facing, what `cfg.X` reads
  Skipping (2) is the bug both PR #25 and PR #38 fixed: the var lives in `.env` (compose variable expansion works fine) but never reaches the running container, so `cfg.X == ""` despite the value being right there in the file. Integration tests can mask this because they wire the field directly via `SetupOpts`. **Real-VPS smoke is the only guard.**
- **Multi-segment chi routes** want `r.Route("/parent", func(r) { r.Method(GET, "/child", h) })` instead of `r.Method(GET, "/parent/child", h)` — the latter probably works (route registers and the handler runs) but the nested form is the chi idiom and matches the rest of `router.go`.
- **`tls_ask` returns 200 only when**: BaseDomain is set AND the asked-about host is `<sub>.<BaseDomain>` (case-insensitive) AND `<sub>` is a single label AND `<sub>` is a real, non-deleted deployment. Anything else is a non-200 so Caddy refuses cert issuance.
- **Proxy.Handler dispatches by Host first, path fallback second.** When `baseDomain` is non-empty AND `r.Host` matches `<sub>.<base>`, route by leftmost label using `r.URL.Path` verbatim. Otherwise fall through to `/d/<name>/...` — internal compose-network calls keep working with the path form even when custom domains are enabled cluster-wide.
- **`adopted` deployments keep their operator-supplied URL** even when BaseDomain is set. They live outside Synapse's DNS scope; rewriting would break the operator's existing setup.

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

## Convex Dashboard embedding contract (PR #26)

The "data / functions / logs / schedules" UI for a single Convex
deployment is the upstream open-source `convex-dashboard`, served
by Synapse as the `convex-dashboard` compose service behind a
Caddy sidecar that strips `X-Frame-Options` + `frame-ancestors`.

The dashboard fork (`dashboard/app/embed/[name]/page.tsx`) iframes
it and replies to `dashboard-credentials-request` postMessage with
`{ type: "dashboard-credentials", adminKey, deploymentUrl, deploymentName }`.
Origin-restricted to the configured `NEXT_PUBLIC_CONVEX_DASHBOARD_URL`.

Don't add credentials to URL hashes/queries — the upstream dashboard
silently ignores them. The handshake is the only auto-login path
that survives `docker pull` of new versions of the upstream image.

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
- [ ] If you added a project / deployment writer: gated via `canAdminProject` / `canEditProject` (NOT a literal `models.RoleAdmin` check)
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
