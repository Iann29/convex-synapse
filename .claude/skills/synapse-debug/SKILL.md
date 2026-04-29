---
name: synapse-debug
description: Diagnose common Synapse failures — CORS errors in the browser, deployments stuck "provisioning", containers that can't reach each other inside the synapse-network, postgres connection refused, dashboard 401 loops, Playwright timing issues. Use when the user reports something in Synapse "isn't working", "is broken", "won't start", or shows error symptoms.
---

# Synapse failure modes & their fixes

Match the symptom, jump to the section.

## Symptoms

- [Browser console: "blocked by CORS"](#cors)
- [Deployment stuck on "provisioning" forever](#stuck-provisioning)
- [`synapse-api` logs: "docker unavailable; provisioning endpoints will fail"](#docker-perms)
- [`synapse-api` logs: "lookup postgres: no such host"](#pg-host)
- [Dashboard: infinite redirect to /login](#login-loop)
- [Playwright "TimeoutError: locator.fill"](#playwright-fill)
- [Playwright "strict mode violation"](#playwright-strict)
- [Provision succeeds but deployment URL returns nothing useful](#deployment-url)
- [`generate_key panicked: attempt to subtract with overflow`](#generate-key-panic)
- [`401 BadAdminKey` from `npx convex` against a Synapse-provisioned deployment](#bad-admin-key)
- [Health worker keeps fighting auto-restart with another node](#double-restart)
- [Provisioning queue not draining; old jobs from a previous test run blocking new ones](#queue-stalled)
- [Test failures only in CI, not local](#ci-only)

---

## CORS

Browser DevTools shows: `Access to fetch at '...' from origin '...' has been
blocked by CORS policy`.

**Cause:** Synapse's CORS middleware is misconfigured or the dashboard is
hitting a path Synapse doesn't reply 204 to on `OPTIONS`.

**Fix:**
1. Verify `SYNAPSE_ALLOWED_ORIGINS` in the synapse container env. Default
   `*` should accept anything.
2. Hit the preflight by hand: `curl -i -X OPTIONS http://localhost:8080/v1/auth/login -H 'Origin: http://localhost:6790' -H 'Access-Control-Request-Method: POST'`. Expect 204 + the four `Access-Control-*` headers.
3. If 204 but the browser still blocks, the request method/header isn't in
   `Access-Control-Allow-Methods` / `-Headers`. Edit
   `synapse/internal/middleware/cors.go`, restart synapse.

## Stuck provisioning

A deployment row sits at `status='provisioning'` with no movement.

**Cause:** Synapse process crashed or was killed mid-provision. The goroutine
that flips status is gone.

**Fix:**
1. Synapse runs an orphan-row sweep at startup. Restart synapse: `docker
   compose restart synapse`. Anything older than 10 min flips to `failed`.
2. If the row is fresher than 10 min, watch synapse logs:
   `docker compose logs -f synapse | grep <deployment-name>`. The Docker
   pull may simply be slow.
3. Worst case: flip the row manually with psql, then `prune-deployments`
   to clean any orphaned container.

## Docker perms

Synapse logs `permission denied while trying to connect to the Docker daemon socket`.

**Cause:** The synapse container can't read `/var/run/docker.sock`. The
distroless image runs as root (intentionally — see Dockerfile comment),
so this typically means the host docker socket is mounted with restrictive
permissions OR the user re-built without the change.

**Fix:**
1. Confirm `synapse/Dockerfile` final image is `distroless/static-debian12`
   WITHOUT `:nonroot`. Rebuild: `docker compose build synapse`.
2. If still failing: `ls -la /var/run/docker.sock` on the host. If owned by
   root with mode 0660, the container needs to share root or the docker GID.

## Pg host

Synapse logs `lookup postgres: no such host`.

**Cause:** Running synapse outside docker compose (e.g. `go run ./cmd/server`
on the host) but `.env` still points at `@postgres:5432`.

**Fix:** Edit `.env` to use `@localhost:5432`. The dev-quickstart in `make dev`
expects this.

## Login loop

Dashboard sends user to `/login` repeatedly even after a successful login.

**Cause:** Either the JWT is being rejected (signature mismatch), or the
auth fetch is throwing an error that the API client interprets as 401.

**Fix:**
1. DevTools → Application → localStorage → check `synapse.auth` exists.
2. Network tab → `/v1/me` request → if 401: token bad. Try clearing
   localStorage and logging in again.
3. If JWT was just rotated (`SYNAPSE_JWT_SECRET` changed), all old tokens
   are invalid. Clear localStorage on every browser session.

## Playwright fill

`TimeoutError: locator.fill: Timeout 10000ms exceeded`.

**Cause:** The locator (`getByLabel` / `#id`) doesn't match anything because
the input has no associated label, or because the page hasn't loaded yet.

**Fix:**
1. Take a screenshot at the point of failure: tests already do this on
   `failure: only-on-failure` mode. Look at the artifact in `test-results/`.
2. Confirm the input has BOTH `id="thing"` AND a `<label htmlFor="thing">`.
3. If the page is server-rendered async, wait explicitly:
   `await expect(page).toHaveURL(/\/teams\b/)` before filling.

## Playwright strict

`Error: strict mode violation: locator resolved to 2 elements`.

**Cause:** `getByText` / `getByRole` matched more than one node. Examples:
- "Create team" empty-state CTA + "Create team" header button
- The same email rendered both in a list row AND a confirmation banner

**Fix:**
- Scope to the parent: `page.getByRole("dialog").getByRole("button", { name: "Create" })`
- Use `exact: true`: `getByRole("button", { name: "Create", exact: true })`
- Use `.first()` if either element is fine

## Deployment URL

Provisioning succeeded, container is `Up`, but `curl http://localhost:3210/version`
returns connection refused or no body.

**Cause:** The Convex backend exposes 3210 internally; the host port mapping
is per-deployment. Multiple deployments can't all be on 3210. Check what
port THIS deployment got.

**Fix:**
```bash
docker ps --filter label=synapse.managed=true --format 'table {{.Names}}\t{{.Ports}}'
```
The host port is the LEFT side of `0.0.0.0:NNN->3210/tcp`.

`/version` itself returns the literal string `unknown` on a healthy Convex
backend — that's not an error.

## Generate-key panic

Synapse logs an error from `Docker.GenerateAdminKey` containing
`thread '<unnamed>' panicked at … fastant-0.1.10/src/tsc_now.rs:170:34: attempt to subtract with overflow`.

**Cause:** Convex's bundled `generate_key` binary depends on `fastant`,
which has a known TSC overflow bug on certain CPUs. It's transient — a
re-run usually succeeds.

**Fix:** Already handled — `internal/docker/admin_key.go::GenerateAdminKey`
retries up to 5x. If you still see deployments fail with this, bump the
retry count (`maxAttempts` constant). Don't try to skip the binary; the
admin key MUST be signed with `INSTANCE_SECRET` to pass `check_admin_key`.

## Bad admin key

`npx convex dev` against a Synapse-managed deployment returns
`401 BadAdminKey: The provided admin key was invalid for this instance`.

**Cause:** The admin key in the DB is not actually signed by the running
backend's `INSTANCE_SECRET`. This happens when the row was seeded by an
older version of Synapse (random hex) or when `generate_key` was bypassed.

**Fix:**
1. Confirm the deployment was created on a Synapse build that includes the
   `generate_key` shell-out. The DB column should look like
   `<deploymentName>|<long-hex>`, not 64-char raw hex.
2. If the row is stale, delete + re-create the deployment (its data volume
   is gone too — there's no in-place fix).
3. If you need to keep the data: stop the container, manually run
   `/convex/generate_key <name> <secret>` on the image, UPDATE the DB row,
   restart.

## Double restart

Two synapse processes both call `Docker.Restart` on the same container
when auto-restart is enabled.

**Cause:** Periodic worker not wrapped in `db.WithTryAdvisoryLock`. Each
node sweeps independently and races the post-CAS restart.

**Fix:** This was the v0.3 bug — should be fixed. Verify the latest
`internal/health/worker.go::tickWithLock` wraps `sweep()` in
`WithTryAdvisoryLock(LockHealthWorker, ...)`.

## Queue stalled

Tests that provision deployments fail intermittently. Logs show
`provisioner: deployment no longer provisioning; cleaning up` for
deployments from a previous test run.

**Cause:** Truncate between tests races with in-flight worker goroutines
that already claimed jobs. The worker is processing dead orphans while
new jobs queue behind.

**Fix:**
1. Verify `Config.Concurrency >= 4` so multiple workers drain in parallel.
2. The pre-check (`SELECT status FROM deployments`) should short-circuit
   orphans into a no-op. Confirm it's present in `runJob` before the
   `Docker.Provision` call.
3. For test reliability, add `provisioning_jobs` to `truncateAll` (it's
   already in the helper), and consider `docker compose restart synapse`
   between full Playwright runs to nuke in-flight goroutines.

## CI only

Tests pass locally but fail in CI.

**Cause possibilities:**
- CI postgres takes longer to come up — increase the health-check `interval`
  / `retries` in the workflow's `services.postgres`.
- Different docker version provisions slower — the e2e timeout might be too
  tight. Bump the test's `timeout` for that case.
- Image cache miss — first deployment in CI cold-pulls the convex backend.
  The `Pre-pull convex-backend image` step in the workflow should warm it;
  if missing, add it.
- Race in worker tests — extend the polling deadline.

When in doubt: trigger the CI manually with `gh run rerun --failed` and
look at the actual logs. Don't speculate.
