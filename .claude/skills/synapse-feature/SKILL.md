---
name: synapse-feature
description: Add a new feature end-to-end across Synapse — backend handler + integration test + dashboard UI + Playwright spec + API docs + commit. Use when the user asks to "add a new endpoint", "build a new feature", "expose X via the API", or any task that crosses backend + frontend boundaries.
---

# Adding a feature to Synapse

Synapse spans Go (backend), Postgres (state), Docker (provisioning), and
Next.js (dashboard). Features touch most of those layers; the convention
below keeps changes consistent and tested.

> **Working on the auto-installer (v0.6) instead?** Don't follow this
> recipe. Bash + bats + the operator-facing CLI live in
> `installer/` and `setup.sh` — see the **synapse-installer** skill
> for the right conventions.
>
> **Working on Aster integration?** Read [`docs/ASTER_INTEGRATION.md`](../../docs/ASTER_INTEGRATION.md)
> first. The `kind=aster` deployment kind is wired through the same
> `Docker.Provision` / `health` / `proxy` paths Convex deployments use,
> just with a different branch — adding a new field there means
> threading it through `models.Deployment.Kind` → `DeploymentSpec.Kind`
> → `Docker.provisionAster` (don't add parallel `if kind == "aster"`
> ladders in the API handlers; dispatch happens at the Docker layer).

## The order to do things

Front-load decisions, then implement and test from the inside out.

### 1. Pick the endpoint shape

Cross-reference Convex Cloud's stable v1 OpenAPI spec at
`https://github.com/get-convex/convex-backend/blob/main/npm-packages/dashboard/dashboard-management-openapi.json`.
If the feature exists in the spec, mirror the path and request/response
shape verbatim — wire compatibility is a north star. If it doesn't,
prefix the path with `/v1` and pick a name with the existing convention
(`POST /v1/teams/{ref}/<verb>` for team-scoped, etc.).

### 2. Write the integration test FIRST

Before coding the handler, add a test file under `synapse/internal/test/`
(package `synapsetest`). Use `Setup(t)` and the existing helpers
(`createTeam`, `createProject`, `RegisterRandomUser`). The test should:

- Cover the happy path
- Cover at least one negative case (401 unauth, 403 wrong role, 409 conflict)
- Use `json.DisallowUnknownFields()` when decoding responses

Run it. It must fail with "404 not found" or "method not allowed"
because the handler doesn't exist yet.

### 3. Implement the handler

Create or extend a file under `synapse/internal/api/`. One file per
resource. Each handler struct exposes `Routes() chi.Router` mounted in
`internal/api/router.go` under the appropriate group (anonymous vs.
authenticated, `/v1` vs. ops).

Use these utilities:

- `writeJSON(w, status, body)`, `writeError(w, status, code, msg)` from `httpx.go`
- `readJSON(r, &dst)` for body parsing (rejects unknown fields, 1MB cap)
- `auth.UserID(r.Context())` to get the caller
- `loadTeamForRequest`, `loadProjectForRequest`, `loadDeploymentForRequest` to
  resolve + assert membership. `loadProjectForRequest` and
  `loadDeploymentForRequest` go through `effectiveProjectRole` so the
  role they return already honours `project_members` overrides — DON'T
  re-query `team_members` directly in a new handler.
- For a fresh project + user pair (e.g. inside a join you're writing
  yourself), use `effectiveProjectRole(ctx, db, projectID, teamID, userID)`
  in `projects.go`.
- `slugify(name)` for slug allocation
- `db.WithRetryOnUniqueViolation(ctx, n, fn)` — wrap any SELECT-then-INSERT
  resource allocator (port, name, slug). UNIQUE-constraint races retry
  transparently. NEVER use `strings.Contains` on the error message — use
  `db.IsUniqueViolationOn(err, "users_email_key")` instead.
- `db.WithTryAdvisoryLock(ctx, pool, key, fn)` — wrap periodic workers so
  multi-node coordination is one round-trip instead of a custom protocol.
  Keys live in `internal/db/advisorylock.go` as constants.
- `audit.Record(ctx, db, audit.Options{...})` — call on every mutating
  success path. Best-effort, never fails the user request.

#### Project-level RBAC gates (v1.0+)

Don't compare `role != models.RoleAdmin` in a new project / deployment
handler. Use the helpers:

- `canAdminProject(role)` — destructive writes (delete, transfer,
  rename, manage members, adopt, upgrade-to-HA, project tokens,
  create deploy key)
- `canEditProject(role)` — non-destructive writes (env vars,
  create deployment)
- Reads (GET project / deployments / env vars / members) — any
  role passes; gate is just "load*ForRequest succeeded"

`viewer` is project-only. Team-level helpers (`models.RoleAdmin` /
`models.RoleMember`) are still right for team-grain code paths
(`loadTeamForRequest` returns the team role unchanged).

If your feature does long async work, do NOT spawn a per-handler goroutine.
Enqueue a row in a job table and run a `Worker` with
`SELECT FOR UPDATE SKIP LOCKED` + parallel goroutines. See
`internal/provisioner/worker.go` for the template.

If your feature touches HA (v0.5+):
- Read replica info via `deployment_replicas`, not `deployments.host_port`.
  The legacy column is kept populated for back-compat but new readers go
  through the replica join (see `proxy.Resolver.ResolveAll` for the pattern).
- Aggregate replica statuses up to deployment status via the same logic
  as `health.Worker.recomputeDeploymentStatus`: any-running wins; failed
  beats stopped on tie.
- Use `crypto.SecretBox.EncryptString` / `DecryptString` for any secret
  that ends up in `deployment_storage`. Never log the plaintext.
- Single-replica deployments leave `HAReplica=false` so `docker.ContainerName`
  returns the legacy `convex-{name}` shape — keeps existing operator
  scripts and dashboards working.

### 4. Run the test until green

`cd synapse && go test ./internal/test/... -run TestYourThing -v -count=1`.
Vet passes too: `go vet ./...`.

### 5. Update the API doc

Add a section to `docs/API.md` with the path, method, body, response,
and a one-line description. Use the ✅/🔧/📍 markers (see existing entries).

### 6. Wire up the dashboard

Add typed methods to `dashboard/lib/api.ts`. If the feature has UI:

- Inline panels go in `dashboard/components/<Thing>Panel.tsx`
- New pages go under `dashboard/app/<route>/page.tsx`
- Reuse primitives from `dashboard/components/ui/`
- ALL labels must have `htmlFor`; ALL inputs must have a stable `id`
  matching their label. Playwright's `getByLabel` and our test reliability
  depend on this.
- Buttons that perform destructive actions: `variant="danger"`,
  `confirm()` before the call, `aria-label` if there are multiple of them
  on a page (so tests can target specific ones).

### 7. Add the Playwright spec

`dashboard/tests/<feature>.spec.ts`. Use `truncateAll()` in `beforeEach`
and the existing helper patterns (register via UI, then drive through the
new flow). Use `dialog.locator(...)` to scope inside modals; never use
loose `getByText` regex when there's any chance of multiple matches.

For features that need TWO users (invites, multi-context auth), use
`browser.newContext()` to get isolated localStorage per side.

### 8. Run everything

```bash
cd /home/ian/convex-2 && docker compose up -d
sleep 4
docker compose build synapse dashboard
docker compose up -d synapse dashboard
sleep 4
PGPASSWORD=synapse psql -h localhost -U synapse -d synapse -c \
  "TRUNCATE users, teams, projects, team_members, deployments, project_env_vars, \
   team_invites, deploy_keys, access_tokens, audit_events, provisioning_jobs, \
   deployment_replicas, deployment_storage \
   RESTART IDENTITY CASCADE;"
docker rm -f $(docker ps -aq --filter label=synapse.managed=true) 2>/dev/null
cd dashboard && npx playwright test --reporter=list
```

All Go + all Playwright must be green.

### 9. Commit

`feat(<scope>): <imperative summary>`. Body: bullets of what's new and the
curl flow you actually ran. Don't push from a feature branch — main is the
working branch in this repo.

### 10. Update the roadmap

Tick the item in `docs/ROADMAP.md` and bump the e2e test count if you
added a new spec.

## Common pitfalls

- **CORS**: if the dashboard is on a different port than the API, browsers
  block requests. The middleware accepts any origin by default; if you
  see a fetch fail, check `OPTIONS` is returning 204.
- **Nullable timestamps**: use `*time.Time` in models when the column is
  nullable, otherwise the JSON shows `"0001-01-01T00:00:00Z"`.
- **Strict-mode locator violations** in Playwright: if `getByRole` hits
  more than one element, scope to a parent (`page.getByRole("dialog")`)
  or use `exact: true`.
- **Deployment provisioning is async**: a `POST create_deployment`
  returns immediately with `status="provisioning"`. The dashboard polls
  every 2s until status flips. Don't assume sync flow in tests — use
  `expect.poll(() => listSynapseContainerNames())`.
