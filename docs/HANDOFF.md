# You are taking over the convex-synapse project

You're stepping into a session where the previous agent + operator just
finished v0.6.x in full — auto-installer, lifecycle commands,
`curl | sh` one-liner, browser first-run wizard. The v0.6 milestone
("first 30 seconds of using Synapse is the installer") is **done**.
What's left is the v1.0 surface area: the hard stuff that turns
"works for one operator on a Hetzner box" into "ships to thousands of
self-hosters across providers".

Read this end-to-end before you write a line of code.

---

## What the project is

**Synapse** = an open-source control plane for self-hosted Convex
deployments. Replicates the closed-source "Big Brain" management plane
that Convex Cloud uses (teams, projects, multi-deployment, audit log,
CLI auth) on infrastructure the operator controls.

- Repo: https://github.com/Iann29/convex-synapse · Apache-2.0 · `main` only
- Working tree: `/home/ian/convex-2/`
- Stack: Go (chi + pgx + docker SDK) backend at `synapse/`, Next.js 16
  + Tailwind 4 dashboard at `dashboard/`, postgres + docker via
  `docker-compose.yml`, pure-bash installer at `setup.sh` + `installer/`
- North-star spec: Convex Cloud's stable v1 OpenAPI in
  `npm-packages/dashboard/dashboard-management-openapi.json` of
  `get-convex/convex-backend`

## Where we are right now

| Milestone | Status |
|---|---|
| v0.1 → v0.5 | ✅ shipped |
| **v0.6 (auto-installer + lifecycle + curl\|sh + first-run wizard)** | **✅ shipped — every chunk merged + real-VPS validated** |
| v0.5.1 (HA polish — upgrade_to_ha worker, real-backend failover, active health probe) | 📋 deferred |
| v0.7 (ghcr.io pre-built images — install ~30s instead of ~3min) | 📋 not started; needs CI workflow + multi-arch buildx |
| ~~v0.6.4 (Cloud images marketplace)~~ | ❌ deprioritized 2026-05-01 |
| **v1.0 (custom domains, S3 backup, RBAC, OIDC, K8s, Helm, API stability)** | 🚀 NEXT — your mission |

Test counts: 305 bats + 146 Go integration + 24 Playwright e2e, all
green in CI on every push. shellcheck `-x` clean across 11 `.sh` files.

A real Hetzner CPX22 has been running this code end-to-end through
every chunk. **That validation surfaced 13 distinct bugs the bats +
Go suites didn't catch** — see
`.claude/skills/synapse-installer/SKILL.md` under "Real-world bugs
caught on the synapse-test VPS". Read it; the lessons generalize.

## Your mission: v1.0 — pick a piece, scope it, ship it

The ROADMAP lists v1.0 items in `docs/ROADMAP.md`. Each one is
substantial enough that the operator has explicitly asked us to
**surface it as a fork-in-the-road instead of just diving in**.
Confirm priority before scoping.

The list:

1. **Custom domains with auto-TLS** — each deployment with its own
   subdomain (e.g. `<deployment>.synapse.example.com`) instead of
   `/d/<name>/*` proxy paths. Caddy on-demand TLS or Let's Encrypt
   per-host. Touches: provisioner (host header allocation), proxy
   (route resolution), caddy.sh (cert reload).
2. **Volume snapshot backups → S3** — extension of v0.6.1 chunk 2.
   Same archive format, but write to a configured S3 bucket on a
   schedule (cron-style) instead of a local tarball. Retention
   policy. Operator opts in via env vars. Touches: lifecycle.sh
   (s3-aware path), new env vars, audit log entries.
3. **RBAC: project-level roles** — admin / member / viewer per
   project (currently roles are team-scoped only). Touches: db
   migration adding `project_members`, every project handler's
   authz check, dashboard role-toggle UI.
4. **OAuth/SSO via OIDC** — works with Authentik, Zitadel,
   Keycloak. Auth handler grows an OIDC discovery flow alongside
   email+password. JWT issuer accepts the OIDC sub claim. Dashboard
   /login adds "Sign in with Provider" button when configured.
   Touches: `internal/auth/`, `internal/api/auth.go`, dashboard auth.
5. **Kubernetes provisioner** — alternative to Docker. Provisioner
   interface already factored (Provision/Destroy/Restart). Add a
   `internal/k8sprov/` that creates Deployment + Service + PVC per
   Synapse deployment. Configured via `SYNAPSE_PROVISIONER=k8s` +
   kubeconfig. Touches: `internal/api/router.go` wiring,
   `internal/health/` (k8s-aware status), Helm chart.
6. **Helm chart** — installs Synapse on an existing K8s cluster.
   Helm umbrella over postgres (CloudNative-PG operator? bitnami?),
   synapse, dashboard, optional cluster-issuer for cert-manager.
   Depends on (5). Touches: new `helm/` dir, GitHub Actions to
   publish to a chart repo on each tag.
7. **Public API stability guarantees + versioned releases** — start
   cutting `v0.7.0`, `v1.0.0` git tags, GitHub releases with notes,
   semver discipline on the OpenAPI shape. The `--upgrade` flow
   already queries `releases/latest` — once tags exist it
   short-circuits to "you're on v1.0.0, latest is v1.0.0" instead
   of always pulling main.

Recommended order (operator can override):
- **A. Cut `v0.6.3` tag + GitHub release first** (5 minutes — `git
  tag v0.6.3 && gh release create`). Closes the loop on `--upgrade`
  having a real target. Cheap, high-leverage. Do this regardless.
- **B. Then v0.7** (ghcr.io images — biggest UX win for next 100
  operators). Install drops from 3min to 30s. CI workflow + multi-
  arch buildx + ghcr publish on tag.
- **C. Then v1.0 items** by operator priority. The list above is in
  rough effort order ascending. RBAC and OIDC are the typical "I
  can't ship to my team without this" items; K8s/Helm is the typical
  "I can't deploy at this scale without this".

## Real-VPS validation discipline

CI (305 bats + 146 Go + 24 Playwright + compose build + shellcheck)
runs on every push and is the **floor**, not the ceiling. The 13
bugs caught during v0.6 all had green CI before real-VPS smoke
surfaced them. Bug classes that bats and Go simply cannot see:

- bash `set -e` footguns (`[[ -n "$X" ]] && cmd` aborting at function tail)
- `trap RETURN` firing on every nested function return (not just the trap-setting function)
- `bash -c "... | psql >/dev/null 2>&1"` swallowing the pipeline's exit code
- `pg_isready` returning 0 during postgres's first-init shutdown cycle
- camelCase vs snake_case API shapes (Convex API uses `accessToken`, `firstRun`, etc.)
- `${SYNAPSE_VERSION}` as docker tag rejecting `/` (branch refs)
- `docker compose images` JSON using `.ContainerName` (not `.Service`)
- compose project-name resolution defying prediction (volume-suffix-match needed)
- `NEXT_PUBLIC_*` build-arg vs runtime env (Next.js inlines at build time)
- iframe / X-Frame-Options / CSP behavior
- the upstream Convex Dashboard's postMessage handshake protocol
- Convex API uses `POST /<resource>/delete` (not HTTP DELETE)
- FK `ON DELETE RESTRICT` on `teams.creator_user_id` blocks user delete

Treat real-VPS smoke as a checkbox in "done" for any change touching
`setup.sh`, `installer/`, `docker-compose.yml`, the Go API surface,
or the dashboard auth/wizard surface.

## The synapse-test VPS — your sandbox

The operator provisioned a Hetzner CPX22 dedicated to integration
testing. Configured in your `~/.ssh/config` as `synapse-vps`. **Free
to break.** The operator has reset access via the Hetzner Cloud
Console — ping them when you bork it badly enough that SSH stops
working.

```bash
ssh synapse-vps                    # alias, keyless, sshd-on-22
# OR
ssh -i ~/.ssh/synapse-test-vps root@<ip>
```

IP, password, key paths all live in `/.vps/credentials.md` (gitignored
under `/.vps/`). Standard real-VPS workflow:

```bash
# Tear down the previous test (or use --uninstall now that it exists)
ssh synapse-vps 'docker rm -f $(docker ps -aq --filter name=synapse-) 2>/dev/null
                 docker volume ls -q | grep -E "(synapse-data-|synapse-pgdata)" \
                   | xargs -r docker volume rm
                 rm -rf /opt/synapse-test'

# Fresh install via curl|sh (the canonical install path now)
ssh synapse-vps 'curl -sSf https://raw.githubusercontent.com/Iann29/convex-synapse/main/setup.sh \
                 | bash -s -- --no-tls --skip-dns-check --non-interactive \
                              --install-dir=/opt/synapse-test'

# Validate from outside the VPS (your dev machine, NOT inside the VPS)
curl -sf http://<vps-ip>:8080/health
curl -sf http://<vps-ip>:8080/v1/install_status   # firstRun should be true
curl -sf -o /dev/null -w "%{http_code}\n" http://<vps-ip>:6790/setup
```

**Hard rules:**

- Never use this VPS for production data. The operator has 3 other VPSes
  for that (prod-Convex, scopuli, kvm4); never touch them.
- Never SSH there for unrelated work — the box is single-purpose.
- If you bork it, ask for a reset; don't try to recover with `rm -rf /`.

## Operator profile

- **Language:** Brazilian Portuguese, informal-technical. Respond in
  pt-BR. Profanity is fine — he uses it casually when hyped.
- **Background:** Engineer-of-software (his words), not a programmer
  in the day-to-day sense. Runs the Amage agency (e-commerce; Next.js
  + Convex self-hosted + BetterAuth). Understands product and concept,
  not implementation details. **Avoid jargon without translating.**
  Use analogies (he liked "shopping center pra Convex", "recepção
  open-source que substitui Big Brain").
- **Legal name:** **Ian Bee** (Iann29 on GitHub), NOT "Ian Saraiva".
  This is in copyright headers / LICENSE files / author lines.
- **Working style:** he likes parallel agents, autonomous slices, and
  pragmatic pushes. He'll say "fica livre", "BORA", "mete marcha",
  "em looping" when he wants you autonomous. He'll say "espera" when
  he wants to pause and discuss. Listen to both.
- **Authorization model:** he's authorized merging PRs without
  per-PR confirmation when CI is green. He still wants the big
  architectural calls (e.g. "should this be its own service or live
  inside synapse-api?") presented as a fork-in-the-road, not as a
  decided plan. **Especially v1.0 items — those each justify a
  scope-then-build conversation, not a dive.**
- **He'll push back on overengineering.** If a feature feels bigger
  than it should, scope it down explicitly. The "less is more"
  framing of v0.6 (we cut v0.6.4 deliberately) is the model.
- **VPS reset is fine but ALWAYS confirm first.** He explicitly said
  "vou auto-aprovar" for merges, but real-VPS destructive ops should
  still get a "vou rodar X, ok?" before fire.
- **When asked "como tá?" / "oq foi feito?":** give a concrete summary
  with numbers (commit count, test count, what's deployed). Brief and
  structured — he's checking, not interrogating.

## Repo conventions (skim once)

Full versions live in `CLAUDE.md` and `AGENTS.md` — both kept under
~250 lines. Highlights:

- **Build green before commit.** `cd synapse && go build ./... && go vet ./... && go test ./... -count=1`
  must pass. Bats + shellcheck for `installer/` changes. Playwright for
  dashboard + handler changes.
- **One feature per commit.** Refactor lives in its own commit.
- **Conventional commits.** `feat(scope):`, `fix(scope):`, `chore:`,
  `docs:`. Bodies are verbose — list the curl flow you actually ran.
- **Push directly to `main`** for small docs / fixes / refactors.
  Large features go through PR (squash merge; keep history clean).
- **Errors to clients** always go through `writeError(w, status, code, msg)`
  in `httpx.go`. Stable codes, human messages, never leak internals.
- **Audit hook** every mutating handler success path. Best-effort,
  never fails the user request.
- **Multi-node patterns** (v0.3 hygiene) are mandatory for new code:
  `db.WithRetryOnUniqueViolation` for resource allocators,
  `db.WithTryAdvisoryLock` for periodic workers, persistent job
  queue (`SELECT FOR UPDATE SKIP LOCKED`) for long async work.
- **URL rewrite contract** (PR #10 + #24): every handler returning
  a `models.Deployment` MUST call `publicDeploymentURL(&d)` before
  `writeJSON`. New handlers in `TeamsHandler` / `ProjectsHandler` need
  the `*DeploymentsHandler` field.
- **bash gotcha**: never use `[[ -n "$X" ]] && cmd` as the last line of
  a function — under `set -e` the test's exit code aborts the caller.
  Use explicit `if`/`fi`. Never `trap RETURN` for cleanup — bash
  fires it on every nested function return. Wrap the inner logic and
  cleanup once on the outer wrapper's return.
- **Lifecycle conventions** (v0.6.1+): see AGENTS.md "v0.6.1 ground
  rules" through "v0.6.3 ground rules" — validate-first, audit-trail,
  no-trap-RETURN, image-tags-vs-version-stamps, suffix-match for
  volumes, set-pipefail in `bash -c`, `SELECT 1` retry for postgres
  readiness, `TRUNCATE … CASCADE` for FK-RESTRICT cleanup.

## Required reading inside the repo

In this order:

1. **`CLAUDE.md`** — repo layout, common commands, conventions, "What HAS
   landed" table. Updated after every milestone. Reflects v0.6 ✅ DONE.
2. **`AGENTS.md`** — cross-tool conventions, "done" checklist, URL
   rewrite contract, v0.6.1/v0.6.2/v0.6.3 ground rules.
3. **`docs/ROADMAP.md`** — every milestone status. v0.6 ✅; v1.0 is
   your target.
4. **`docs/ARCHITECTURE.md`** — design decisions and what's deliberately
   out of scope.
5. **`.claude/skills/synapse-installer/SKILL.md`** — bash conventions,
   the 13 real-world bugs caught during v0.6, the canonical real-VPS
   smoke recipe.
6. **`docs/PRODUCTION.md`** — the operator-facing install guide.
   Already leads with `curl | sh`.
7. **`docs/SCREENSHOTS.md`** — UI tour. Add a `/setup` wizard
   screenshot when you do anything UX-side.
8. **`docs/V0_6_INSTALLER_PLAN.md`** — the full v0.6 design + chunk
   landing log. Anti-features still apply going forward.

When in doubt, `git log --oneline | head -50` shows the journey. Commit
bodies are intentionally verbose — they explain trade-offs not restated
in the code.

## What's deliberately out of scope (forever)

- Stripe/Orb billing parity
- WorkOS-specific SAML paths (we use OIDC, see v1.0 list)
- Multi-region / deployment classes
- Discord/Vercel integrations
- LaunchDarkly equivalent

If you're tempted to add one, move it to "maybe never" in ROADMAP.

## Pointers

- North-star spec: `npm-packages/dashboard/dashboard-management-openapi.json`
  in `get-convex/convex-backend`
- Convex backend lease (single-writer-per-deployment, design constraint):
  `crates/postgres/src/lib.rs:1738-1799` of the Convex repo. Active-
  passive HA per deployment is possible (Postgres + S3 + LB);
  active-active isn't
- Convex self-hosted dashboard source (`postMessage` auto-login
  protocol): `npm-packages/dashboard-self-hosted/src/pages/_app.tsx`
  in `get-convex/convex-backend`
- Self-hosted docs: https://docs.convex.dev/self-hosting

## Final notes

- Cut `v0.6.3` git tag + GitHub release as your warm-up — closes the
  loop on `--upgrade` having a real target. Should be your first
  commit on a new session.
- The operator gave the synapse-test VPS as your sandbox. Use it.
- v1.0 items each justify a scope-then-build conversation. Don't
  dive into K8s or OIDC without the operator confirming priority —
  each is a multi-session feature.
- Subagents are valuable for research-heavy work and parallel
  independent slices. Lean on them when the question spans more than
  a single file or branches into "what does the upstream do?".
- The operator wants 5k stars eventually. The path there is
  "first 30 seconds of using Synapse is the installer" (✅ done in
  v0.6) + "every operator pain point in self-hosting Convex is
  something we already solved" (your job in v1.0).

Start by reading `CLAUDE.md`, `AGENTS.md`, `docs/ROADMAP.md`, and the
`synapse-installer` skill end-to-end. Then say hi to the operator in
pt-BR and confirm where to begin (cut the tag, then ask which v1.0
item to scope first). BORAAAA MESTREEEEE!! vamos lá.
