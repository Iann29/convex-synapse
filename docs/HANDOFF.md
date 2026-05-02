# HANDOFF — close the OpenAPI gap

You're stepping into a session where the previous agent shipped:

- ✅ **v0.6 complete** — auto-installer, lifecycle commands, `curl | sh`, first-run wizard, tagged as `v0.6.3` on GitHub Releases.
- ✅ **v1.0 custom domains** — `SYNAPSE_BASE_DOMAIN`, Caddy on-demand TLS, `/v1/internal/tls_ask` gate, host-header proxy routing. Real-VPS validated.
- ✅ **v1.0 S3 backup** — `setup.sh --backup --to-s3=s3://...` and `--restore=s3://...` via `curl --aws-sigv4`. AWS + S3-compatible (Backblaze, R2, Wasabi, MinIO).

Test counts: **305 bats + 146 Go integration + 24 Playwright e2e**, all green in CI on every push. shellcheck `-x` clean across 12 `.sh` files.

**Your mission, one sentence:** push OpenAPI compatibility from ~70-75% to **as close to 100% of the self-hosted-relevant subset as possible**, in priority order.

Read this end-to-end before you write a line of code. The operator wrote: "queremos 100% PORRA!! DEU DE PREGUIÇA". Match the energy — but be honest about which endpoints are deliberately out of scope (~60+ paths require billing/SSO/Vercel parity that the ROADMAP explicitly cuts).

---

## What "100%" actually means

The Convex Cloud OpenAPI spec at `npm-packages/dashboard/dashboard-management-openapi.json` (in `get-convex/convex-backend`) has **113 paths total**. Categorized:

- **~30 already implemented** — see `synapse/internal/api/router.go`.
- **~70 hard out-of-scope** — billing (Orb/Stripe), SSO (WorkOS), Discord/Vercel/OAuth-app integrations, cloud-managed backups, periodic cloud backups, referral codes. The ROADMAP "Maybe never" section excludes these by design. Pretending we'll match them is a trap; tell the operator "permanently cut" if asked.
- **~13 in-scope but missing** — your target. The list below.

The ceiling is **~95% strict OpenAPI coverage**, but **100% of the open-source, self-hosted-relevant subset**. Frame it that way to the operator — he wants the latter, not the former. The scorecard in `docs/ROADMAP.md` should reflect "100% of the in-scope subset, ~5% of paths intentionally cut" when you're done.

---

## The endpoints to ship (in priority order)

### Priority 1 — Quick wins (~30 min each)

These are 6 small handlers that close visible gaps. Operator can usually find a workaround today (TRUNCATE, dashboard manual edits) but each one removes a "wait, why is this not a thing?" moment.

#### 1. `POST /v1/projects/{id}/transfer` — move project between teams

**Spec:**
```json
{
  "operationId": "transfer_project",
  "request": { "destinationTeamId": "<TeamId>" },
  "response": "204 No Content"
}
```

**Implementation:**
- Mount on `ProjectsHandler` next to `delete`.
- Authz: caller must be admin of BOTH the source AND destination teams.
- DB: single `UPDATE projects SET team_id = $1 WHERE id = $2`.
- Audit: `audit.ActionTransferProject` (add to vocabulary if missing).
- Edge case: project with deployments — they live under projects, no schema change needed; deployments_replicas + deployment_storage cascade fine.
- Test in `internal/test/projects_test.go`: happy path + non-admin-source 403 + non-member-dest 403 + project-not-found 404.

#### 2. `PUT /v1/projects/{id}` — update project (name + slug)

**Spec:**
```json
{
  "operationId": "update_project",
  "request": { "name": "string?", "slug": "string?" },
  "response": "204 No Content"
}
```

**Implementation:**
- We already have rename via `POST /v1/projects/{id}/update`. Add a `PUT /v1/projects/{id}` that accepts name+slug optional.
- Slug uniqueness is per-team — wrap in `db.WithRetryOnUniqueViolation`.
- Authz: team admin or project owner.
- Test: name-only update / slug-only update / both / slug conflict 409.

#### 3. `POST /v1/teams/{ref}` — update team

**Spec:**
```json
{
  "operationId": "update_team",
  "request": { "name": "string?", "slug": "string?", "defaultRegion": "string?" },
  "response": "200"
}
```

**Implementation:**
- We don't have an update_team handler at all. Add it next to `create_team`.
- Authz: team admin only.
- Slug uniqueness is global (`citext` unique constraint already exists).
- `defaultRegion` is just a string — Synapse doesn't multi-region today; store and return it but document it has no behavioural effect.
- Test: name update / slug update / non-admin 403 / slug conflict 409.

#### 4. `POST /v1/teams/{ref}/delete` — delete team

**Spec:** no request body, `200 OK` response.

**Implementation:**
- The wizard cleanup uses `TRUNCATE users CASCADE` because there was no API. Replace that with a real `DELETE FROM teams WHERE id = $1` once this lands.
- Authz: team admin only.
- FK `ON DELETE RESTRICT` on `teams.creator_user_id` is BACKWARD-facing (users can't be deleted while they're a team creator). Teams CAN be deleted; their projects/deployments cascade.
- Caveat: deployment containers don't auto-cascade. Delete the team → orphan containers in Docker. Two options:
  - (recommended) Require all deployments deleted first, return 409 with `team_has_deployments`. Fail-fast, less footgun.
  - Or mirror `lifecycle::uninstall` step 2: `docker rm -f $(docker ps -aq --filter label=synapse.team=$team_id)`.
- After this lands, update `installer/install/verify.sh` to use the proper API instead of TRUNCATE.

#### 5. `POST /v1/teams/{ref}/update_member_role`

**Spec:**
```json
{
  "operationId": "update_member_role",
  "request": { "memberId": "<MemberId>", "role": "admin|developer" },
  "response": "200"
}
```

**Implementation:**
- We track `team_members.role` already (admin / member). The spec uses `developer`; map that to our `member` for storage. Document the alias.
- Authz: team admin only.
- Refuse demoting the last admin — return 409 `last_admin` (otherwise team becomes unrecoverable).
- Test: promote/demote / 403 non-admin / 409 last admin / 404 unknown member.

#### 6. `PUT /v1/me/update_profile_name` + `POST /v1/me/delete_account`

**Spec:**
- update: `{ "name": "string" }` → 204
- delete: no body → 200

**Implementation:**
- Both on `MeHandler` (`internal/api/me.go`). Add `Routes()` entries.
- update: trivial UPDATE on `users.name`.
- delete: caller must not be the last admin of any team they belong to (same `last_admin` reason as #5). If clean, `DELETE FROM users WHERE id = $1` — CASCADE handles team_members, access_tokens, etc; SET NULL on audit_events.actor_id and projects.creator_user_id.
- Test: update happy / delete happy / delete-while-last-admin 409.

### Priority 2 — Project/team-scoped access tokens (1 session)

Today only **personal** access tokens (`/v1/create_personal_access_token`). The spec has THREE more scopes:

- `GET /v1/projects/{id}/access_tokens` — tokens scoped to a project (CI/CD that should only deploy this one project)
- `GET /v1/teams/{ref}/access_tokens` — tokens scoped to a team (tooling that should only act inside this team)
- `GET /v1/deployments/{name}/access_tokens` — tokens scoped to a single deployment
- `GET /v1/projects/{id}/app_access_tokens` — separate tier for "app" tokens (preview deploy keys: short-lived, single deployment)

**Implementation:**
- Schema: extend `access_tokens` table with `scope_type` (personal/team/project/deployment/app), `scope_id` (nullable; team_id / project_id / deployment_id depending), `permissions` (text[] or JSON, optional). Migration `000007_scoped_tokens.up.sql`.
- Handler: extend `AccessTokensHandler.Routes()` to accept `POST /v1/teams/{ref}/access_tokens` (create), `GET /v1/teams/{ref}/access_tokens` (list), and similar for projects + deployments.
- Auth middleware: when validating an opaque PAT, check the request's scope vs the token's. Project-scoped token hitting `/v1/teams/...` 403s.
- Convex CLI uses `cli_credentials` (signed admin key, separate path) — so this doesn't break npx convex.
- Test: create at each scope / list at each scope / scope-violation 403 / expiry honoured.

### Priority 3 — Already-roadmapped (DON'T dive without explicit go-ahead)

These have their own v1.0 chunks and are bigger than "OpenAPI gap":

- **RBAC project-level roles** (`get_project_roles_for_team`, `update_project_roles`) — see ROADMAP. Touches schema (project_members), every project handler's authz, dashboard role-toggle. Operator hasn't picked this up yet; check before scoping.
- **OIDC / SSO** (`/identities`, `/list_identities`, `/unlink_identity`, `/authorize`, `/authorize_app`) — works with Authentik / Zitadel / Keycloak. Touches `internal/auth/`, dashboard `/login`. Bigger lift; check before scoping.

### Priority 4 — Probably skip but document

- `GET /v1/member_data` — looks like an alias for `/v1/me`. Implement as a thin forwarder (1 line) so any tool that hardcodes this path works.
- `/v1/profile_emails/*` — multi-email per profile. Useful for SaaS, not for self-hosted (operator owns the box, one email is fine). Document as deliberately-skipped in the scorecard.
- `GET /v1/optins` — TOS / marketing opt-in flags. Return `[]` always; document.

### Hard out of scope — DO NOT IMPLEMENT

If you're tempted to do these, stop and re-read the ROADMAP "Maybe never" section. The operator has explicitly cut them.

| Path prefix | Why cut |
|---|---|
| `/cloud_backups/*` | Convex Cloud's hosted backup product. Synapse's `--backup` is the equivalent. |
| `/deployments/*/configure_periodic_backup`, `disable_periodic_backup`, etc | Same — managed cloud backups. |
| `/discord/*` | Discord integration. |
| `/teams/*/oauth_apps/*`, `/authorize_app` | OAuth app registration (Convex's own product). |
| `/teams/*/cancel_orb_subscription`, `change_subscription_plan`, etc (12+ paths) | Stripe/Orb billing. |
| `/teams/*/list_invoices`, `current_spend`, `get_entitlements`, `failed_payment`, etc | Billing/usage tracking. |
| `/teams/*/disable_sso`, `enable_sso`, `update_sso`, etc (5+ paths) | Enterprise SSO via WorkOS. |
| `/teams/*/workos_*`, `/workos/*` (15+ paths) | WorkOS integration. |
| `/teams/*/apply_referral_code`, `validate_referral_code`, `get_discounted_plan` | Marketing/referrals. |
| `/vercel/*` | Vercel integration. |
| `/teams/*/list_active_plans`, `get_orb_subscription`, `create_subscription`, `create_setup_intent` | Billing plans. |

When the dashboard or a CLI hits one of these, return `404 not_found` with code `not_supported_in_self_hosted` and a message pointing at `docs/ARCHITECTURE.md` "out of scope" section. Don't 501 — that confuses tools that retry.

Decision: write a single middleware that intercepts every `not_supported_in_self_hosted` path and returns the structured 404. Add the list to `internal/api/router.go` as `notSupportedPaths`.

---

## How to actually ship each endpoint

Pattern, end-to-end:

1. **Schema** (only if needed): new migration in `synapse/internal/db/migrations/`. `up.sql` + `down.sql`. Embedded via `go:embed`.
2. **Handler** in `synapse/internal/api/<resource>.go`. Add to the existing `Routes() chi.Router` of the resource. Use `r.Method(http.MethodPut, ...)` for PUT. **For multi-segment paths use `r.Route("/parent", func(r) { r.Method(...) })` — chi has subtle behaviour on multi-segment `r.Method`.**
3. **Authz**: lean on existing `loadTeamForRequest` / `loadProjectForRequest` / `loadDeploymentForRequest` helpers. They handle the membership check.
4. **Audit**: every mutating handler calls `audit.Record(ctx, db, audit.Options{...})` on the success path. Add new action constants to `internal/audit/audit.go` if needed (keep them in line with Convex Cloud's vocabulary — `transferProject`, `updateTeam`, etc).
5. **Errors**: `writeError(w, status, code, msg)` from `httpx.go`. Stable codes (`team_has_deployments`, `last_admin`, `slug_taken`, etc), human messages.
6. **URL rewrite contract**: every handler that returns a `models.Deployment` MUST call `h.publicDeploymentURL(&d)` before `writeJSON`. New project/team handlers that include nested deployment data need the same.
7. **Integration test** in `synapse/internal/test/<resource>_test.go`: happy path + each authz failure + each business-rule failure. Use `h.DoJSON(...)` and `assertEq` on the response shape.
8. **Audit test**: read the latest `audit_events` row and assert action + actor + target.
9. **API doc**: append the endpoint to `docs/API.md` (one paragraph: verb, path, request body, response, error codes). Cross-reference the OpenAPI operationId so tooling can match.
10. **Dashboard**: ONLY if the endpoint is operator-visible (e.g. `update_member_role` needs a UI; `transfer_project` could be a dialog or skipped initially). Bigger PRs can defer dashboard work to a follow-up; operator will tell you if it's blocking.
11. **Real-VPS smoke**: any handler that emits a URL or touches the deployment lifecycle. The 13-bug list below is what real-VPS catches that CI can't.

---

## Real-VPS validation discipline

CI is the **floor**, not the ceiling. The 13 distinct bug classes that CI missed during v0.6 → v1.0:

- bash `set -e` footguns (`[[ -n "$X" ]] && cmd` aborting at function tail)
- `trap RETURN` firing on every nested function return (not just the trap-setting function)
- `bash -c "... | psql >/dev/null 2>&1"` swallowing the pipeline's exit code (no `set -o pipefail` inheritance)
- `pg_isready` returning 0 during postgres's first-init shutdown cycle (need `SELECT 1` retry instead)
- camelCase vs snake_case API shapes (Convex uses `accessToken`, `firstRun`, `memberId`)
- `${SYNAPSE_VERSION}` as docker tag rejecting `/` (branch refs)
- `docker compose images` JSON using `.ContainerName`, NOT `.Service`
- compose project-name resolution defying prediction (suffix-match volumes by `synapse-pgdata$` instead)
- `NEXT_PUBLIC_*` build-arg vs runtime env (Next.js inlines at build time)
- iframe / X-Frame-Options / CSP behavior
- Convex Dashboard's postMessage handshake protocol
- Convex API uses `POST /<resource>/delete`, NOT HTTP `DELETE`
- FK `ON DELETE RESTRICT` on `teams.creator_user_id` blocks user delete

**The 3-point env wire-up rule** (every new `SYNAPSE_*` env var):
1. `installer/templates/env.tmpl` — operator-facing, what they see in `.env`
2. `docker-compose.yml` synapse service `environment:` block — container-facing, what reaches the binary
3. `synapse/internal/config/config.go` — Go-facing, what `cfg.X` reads

Skipping (2) is the bug PR #25 and PR #38 both fixed. Integration tests can mask it because they wire the field directly via `SetupOpts`. **Real-VPS smoke is the only guard.**

Real-VPS smoke is part of "done" for any change that touches `setup.sh`, `installer/`, `docker-compose.yml`, the Go API surface, or the dashboard auth/wizard surface.

---

## The synapse-test VPS — your sandbox

Hetzner CPX22, dedicated to integration testing. Configured as `synapse-vps` in your `~/.ssh/config`. **Free to break.** The operator has reset access via Hetzner Cloud Console — ping if SSH stops working.

```bash
ssh synapse-vps                    # alias, keyless, sshd-on-22
```

IP, password, key paths in `/.vps/credentials.md` (gitignored under `/.vps/`). Standard workflow:

```bash
# Tear down the previous test
ssh synapse-vps '
  bash /opt/synapse-test/setup.sh --uninstall --install-dir=/opt/synapse-test --non-interactive --skip-backup 2>/dev/null
  docker rm -f $(docker ps -aq --filter name=synapse- --filter label=synapse.managed=true) 2>/dev/null
  docker volume ls -q | grep -E "(synapse-data-|synapse-pgdata)" | xargs -r docker volume rm
  rm -rf /opt/synapse-test
'

# Fresh install on your branch
ssh synapse-vps '
  curl -sSf https://raw.githubusercontent.com/Iann29/convex-synapse/<your-branch>/setup.sh \
    | SYNAPSE_BOOTSTRAP_REF=<your-branch> bash -s -- \
        --no-tls --skip-dns-check --non-interactive \
        --install-dir=/opt/synapse-test
'

# Validate from outside (your dev machine)
curl -sf http://<vps-ip>:8080/health
curl -sf http://<vps-ip>:8080/v1/install_status
```

**Hard rules:**

- Never use this VPS for production data. Operator has 3 other VPSes (prod-Convex, scopuli, kvm4) — never touch them.
- Single-purpose. Don't SSH for unrelated work.
- Borked it? Ask for a reset. Don't `rm -rf /`.

---

## Operator profile

- **Language:** Brazilian Portuguese, informal-technical. Respond in pt-BR. Profanity casual when hyped ("PORRA", "BORA").
- **Background:** Engineer-of-software (his words), not a programmer day-to-day. Runs the Amage agency (e-commerce, Next.js + Convex self-hosted + BetterAuth). Understands product and concept; **avoid jargon without translating**. Use analogies.
- **Legal name:** **Ian Bee** (Iann29 on GitHub), NOT "Ian Saraiva". This is in copyright headers / LICENSE / author lines.
- **Working style:** loves parallel agents, autonomous slices, pragmatic pushes. "BORA", "mete marcha", "em looping" = full autonomy. "espera" = pause and discuss.
- **Authorization model:** PRs merge without per-PR confirmation when CI is green. Architectural calls (e.g. "should this be its own service?") want fork-in-the-road framing.
- **Pushes back on overengineering.** ROADMAP "Maybe never" is sacred — don't try to sneak billing/SSO back in.
- **VPS is a sandbox** — full access, but **always confirm before destructive ops**. He explicitly said this.
- **"Como tá?" / "oq foi feito?":** concrete summary with numbers (PR count, test count, what's deployed). Brief.
- **Quote from this session:** "queremos 100% PORRA!! DEU DE PREGUIÇA CARALHOOOOOOO". The energy is real — match it.

---

## Repo conventions (skim once)

Full versions in `CLAUDE.md` and `AGENTS.md`. Highlights:

- **Build green before commit.** `cd synapse && go build ./... && go vet ./... && go test ./... -count=1` (~25s). Bats + shellcheck for `installer/`. Playwright for dashboard + handler changes.
- **One feature per commit.** Refactor in its own commit.
- **Conventional commits.** `feat(scope):`, `fix(scope):`, `chore:`, `docs:`. Bodies verbose — list the curl flow you actually ran.
- **Push directly to `main`** for small docs / fixes / refactors. Large features go through PR (squash merge; clean history).
- **Errors to clients** through `writeError(w, status, code, msg)` in `httpx.go`. Stable codes, human messages, never leak internals.
- **Audit hook** every mutating handler success path. Best-effort, never fails the user request.
- **Multi-node patterns** (v0.3 hygiene) mandatory: `db.WithRetryOnUniqueViolation` for resource allocators, `db.WithTryAdvisoryLock` for periodic workers, persistent job queue (`SELECT FOR UPDATE SKIP LOCKED`) for long async work.
- **bash gotcha:** never `[[ -n "$X" ]] && cmd` as last line of a function under `set -e`. Never `trap RETURN` for cleanup. Use `r.Route()` for multi-segment chi paths. Add `set -o pipefail` explicitly inside `bash -c`. Wait for `psql -tAc 'SELECT 1'` not `pg_isready`.
- **Lifecycle commands** live in `installer/install/lifecycle.sh`. New ones follow the wrapper / `_inner` pattern with manual rm-of-stage-dir on the wrapper's return.

---

## Required reading inside the repo

In this order:

1. `CLAUDE.md` — repo layout, common commands, conventions, "What HAS landed" table.
2. `AGENTS.md` — cross-tool conventions, "done" checklist, URL rewrite contract, v0.6 + v1.0 ground rules.
3. `docs/ROADMAP.md` — every milestone status. v0.6 ✅, v1.0 in progress.
4. `docs/API.md` — operator-facing endpoint reference. Append your new endpoints here.
5. `.claude/skills/synapse-installer/SKILL.md` — bash conventions, the 13 real-world bugs, real-VPS smoke recipe.
6. `docs/ARCHITECTURE.md` — design decisions and what's deliberately out of scope.

When in doubt, `git log --oneline | head -50` shows the journey. Commit bodies intentionally verbose.

---

## Pointers

- North-star spec: `npm-packages/dashboard/dashboard-management-openapi.json` in `get-convex/convex-backend`. Refetch via `curl -sf https://raw.githubusercontent.com/get-convex/convex-backend/main/npm-packages/dashboard/dashboard-management-openapi.json -o /tmp/convex-openapi.json`.
- Convex backend lease (single-writer-per-deployment, design constraint): `crates/postgres/src/lib.rs:1738-1799` of the Convex repo. Active-passive HA per deployment is possible (Postgres + S3 + LB); active-active isn't.
- Convex self-hosted dashboard source: `npm-packages/dashboard-self-hosted/src/pages/_app.tsx` in `get-convex/convex-backend`.
- Self-hosted docs: https://docs.convex.dev/self-hosting

---

## How to plan your first session

1. Read `CLAUDE.md` + `AGENTS.md` + this file.
2. Refetch the OpenAPI spec to `/tmp/convex-openapi.json`.
3. Spawn the work as one-PR-per-priority-tier — cleaner for review:
   - **PR 1**: Priority 1 endpoints 1-6 (transfer, update_project, update_team, delete_team, update_member_role, profile-name + delete-account). One commit per endpoint.
   - **PR 2**: Priority 2 — scoped access tokens (schema migration + handlers + middleware + tests).
   - **PR 3**: Priority 4 — `member_data`, `optins`, `not_supported_in_self_hosted` middleware, scorecard update in ROADMAP.
4. Each PR ends with a real-VPS smoke against `synapse-vps`. If you smoke a handler that emits a URL, verify via curl that the URL form is correct (publicDeploymentURL contract).
5. Update `docs/API.md` AS YOU GO. Don't batch the docs work to the end.
6. Update `docs/ROADMAP.md` "Compatibility scorecard" at the end of EACH PR. This is the operator-visible north-star metric.
7. After all 3 PRs land, write a follow-up commit that updates the scorecard's "Auth/Teams/Projects/Deployments" coverage % to reflect "100% of in-scope, ~5% of paths intentionally cut".

---

## What's deliberately out of scope (forever)

- Stripe/Orb billing parity (irrelevant for self-hosted)
- WorkOS-specific SAML paths (we have email+password JWT; OIDC is on the roadmap)
- Multi-region / deployment classes
- Discord/Vercel/etc integrations
- LaunchDarkly equivalent
- Cloud-managed backups (`/cloud_backups/*`)
- Periodic cloud backups (`/deployments/*/configure_periodic_backup`)
- OAuth app registration (`/teams/*/oauth_apps/*`)

If a tool depends on these, document the gap in `docs/ARCHITECTURE.md` and tell the operator. Don't try to fake them; the failure mode of "billing endpoint returns 200 with bogus data" is worse than "404 not_supported".

---

## Final notes

- The operator is hyped: "DEU DE PREGUIÇA". Match the energy with disciplined, parallelizable work. Don't bloat scope.
- v1.0 is in progress. After this OpenAPI push lands, the roadmap-blockers are RBAC and OIDC. Don't dive without the operator confirming.
- Subagents are valuable for parallel slices. Spawn one per priority-tier PR if context budget allows.
- The 5k-stars goal is alive: "first 30 seconds of using Synapse is the installer" + "every operator pain point in self-hosting Convex is something we already solved". Drop-in OpenAPI is a step toward the second.

Start by reading `CLAUDE.md`, `AGENTS.md`, `docs/ROADMAP.md`, and refetching the OpenAPI spec. Then say hi to the operator in pt-BR and confirm priority order (he wants P1 first, but ask if anything has shifted). BORA, MESTRE.
