# You are taking over the convex-synapse project

You're stepping into a session where the previous agent + operator just
finished v0.6.0 (the auto-installer milestone), four fix-ups closing the
public-URL rewrite chain (PRs #23/#24/#25/#26), and an unplanned
brought-forward fix that hosts the open-source Convex Dashboard
alongside Synapse with auto-login. Read this end-to-end before you
write a line of code.

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
| **v0.6.0 (auto-installer)** | **✅ shipped — 10/10 chunks merged** |
| **v0.6.0 fix-ups (#17, #23, #24, #25, #26)** | ✅ all merged |
| **v0.6.1 (lifecycle commands)** | 🚀 NEXT — your mission |
| v0.6.2 (hosted `curl \| sh`) | 📋 trivial once a static host exists |
| v0.6.3 (browser first-run wizard) | 📋 lighter than it sounds now that Convex Dashboard auto-login works |
| v0.7 (cloud images) | 📋 |
| v1.0 | distant |

Test counts: ~136 Go integration + 20 Playwright e2e + 211 bats unit, all
green in CI on every push. shellcheck `-x` clean across 9 `.sh` files.

A real Hetzner CPX22 has been running this code end-to-end through every
chunk and fix-up. **That validation surfaced 9 distinct bugs the bats +
Go suites didn't catch** — the whole list lives in `.claude/skills/synapse-installer/SKILL.md`
under "Real-world bugs caught on the synapse-test VPS". Read it; the
lessons generalize.

## Your mission: v0.6.1 lifecycle commands

`setup.sh` already has the flag surface reserved for these commands —
each one currently exits 2 with "not yet implemented". Implement the
runtime behind each flag, in this priority order. Each one should be
its own PR (sized small enough to review in one sitting):

1. **`--doctor`** — already implemented. Runs preflight against an existing
   install with no mutations. Skip — keep as a smoke test for new code.

2. **`--upgrade`** (highest priority). `git pull` (or `tar` extract from
   the upgrade payload) → `docker compose pull` → `up -d --build` →
   wait `/health` 2xx. On failure, roll back to the previous image
   tag + restart. Audit trail in the install log. Real-VPS validation
   is a hard requirement — this is the command operators run on
   production, with state.

3. **`--backup` / `--restore`** (do these together — they share format).
   `pg_dump synapse-postgres` + `tar` of every `synapse-data-*` volume
   into a single timestamped archive (suggested:
   `synapse-backup-YYYYMMDD-HHMMSS.tar.gz`). `--restore <path>`
   reverses: `docker compose down`, replaces volumes from the tarball,
   `up -d`, smoke-test. Operator gets a known-good rollback point
   before any risky upgrade.

4. **`--uninstall`** — mandatory `--backup` prompt first (set
   `--skip-backup` to override). Then `docker compose down --volumes`
   (only if operator confirms), `caddy::remove_block` from
   `/etc/caddy/Caddyfile` if it was a host-Caddy install, remove the
   install dir.

5. **`--logs <component>`** — thin wrapper over
   `docker compose logs -f <service>`. Components:
   `synapse / dashboard / postgres / caddy / convex-dashboard`.

6. **`--status`** — diagnostic snapshot: container states, port bindings,
   DNS resolution result, TLS cert expiry (when caddy_host), disk usage
   under `/var/lib/docker`. Read-only, prints a structured table. Same
   output shape as the success screen at the end of a fresh install.

For each: write the function in `installer/install/lifecycle.sh` (new
file), wire into `setup.sh` (replacing the 501-style stubs), add bats
unit tests in `installer/test/install/lifecycle.bats`, and **smoke on
the synapse-test VPS** before declaring done.

The `synapse-installer` skill has the conventions; follow them.

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
under `/.vps/`; the directory + file already exist on the operator's
machine — read them, don't ask). Standard real-VPS workflow:

```bash
# Tear down the previous test
ssh synapse-vps 'docker compose -f /opt/synapse-test/docker-compose.yml down -v 2>/dev/null
                 docker rm -f $(docker ps -aq --filter label=synapse.managed=true) 2>/dev/null
                 rm -rf /tmp/convex-synapse /opt/synapse-test'

# Clone the branch under test and run setup.sh end-to-end
ssh synapse-vps 'cd /tmp && git clone -b <your-branch> https://github.com/Iann29/convex-synapse.git
                 cd convex-synapse && bash setup.sh --no-tls --skip-dns-check --non-interactive --install-dir=/opt/synapse-test'

# Validate from outside the VPS (your dev machine, NOT inside the VPS)
curl -sf http://<vps-ip>:8080/health
curl -sf -o /dev/null -w "%{http_code}\n" http://<vps-ip>:6790/register
```

**Hard rules:**

- Never use this VPS for production data. The operator has 3 other VPSes
  for that (prod-Convex, scopuli, kvm4); never touch them.
- Never SSH there for unrelated work — the box is single-purpose.
- If you bork it, ask for a reset; don't try to recover with `rm -rf /`.

## Real-VPS validation discipline

CI (~131 Go + 20 Playwright + 211 bats) runs on every push and is the
floor, not the ceiling. The 9 bugs caught during v0.6.0 + fix-ups all
had green CI before real-VPS smoke surfaced them. Bug classes that bats
and Go simply cannot see:

- bash `set -e` footguns (`[[ -n "$X" ]] && cmd` aborting at function tail)
- `docker compose pull` on services with `build:`
- camelCase vs snake_case API shapes (Convex's API is camelCase: `accessToken`, `projectId`, `convexUrl`)
- `NEXT_PUBLIC_*` build-arg vs runtime env (Next.js inlines at build time)
- missing host tooling (`jq`, `dig`)
- public-IP / DNS / TLS / Let's Encrypt flows
- iframe / X-Frame-Options / CSP behavior
- the upstream Convex Dashboard's postMessage handshake protocol

Treat real-VPS smoke as a checkbox in "done" for any change touching
`setup.sh`, `installer/`, `docker-compose.yml`, or any backend handler
that emits a URL.

## Operator profile

- **Language:** Brazilian Portuguese, informal-technical. Respond in
  pt-BR. Profanity is fine — he uses it casually when hyped.
- **Background:** Engineer-of-software (his words), not a programmer
  in the day-to-day sense. Runs the Amage agency (e-commerce; Next.js
  + Convex self-hosted + BetterAuth). Understands product and concept,
  not implementation details. **Avoid jargon without translating.**
  Use analogies (he liked "recepção open-source que substitui Big Brain").
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
  decided plan.
- **He'll push back on overengineering.** Anti-features in
  `docs/V0_6_INSTALLER_PLAN.md` are anti-features for a reason; don't
  invent new ones.
- **When asked "como tá?" / "oq foi feito?":** give a concrete summary
  with numbers (commit count, test count, what's deployed). Brief and
  structured — he's checking, not interrogating.

## Repo conventions (skim once)

Full versions live in `CLAUDE.md` and `AGENTS.md` — both <250 lines,
both worth reading end-to-end before non-trivial changes. Highlights:

- **Build green before commit.** `cd synapse && go build ./... && go vet ./... && go test ./... -count=1`
  must pass. Bats + shellcheck for `installer/` changes. Playwright for
  dashboard + handler changes.
- **One feature per commit.** Refactor lives in its own commit.
- **End-to-end test each slice.** New endpoint → integration test in
  `synapse/internal/test/<resource>_test.go`. New UI flow → Playwright
  spec. New installer phase → bats. **No "tested manually with curl"
  for anything user-visible.**
- **Conventional commits.** `feat(scope):`, `fix(scope):`, `chore:`,
  `docs:`. Bodies are verbose — list the curl flow you actually ran.
- **Push directly to `main`** for small docs / fixes / refactors. Large
  features go through PR (squash merge; keep history clean).
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
  Use explicit `if`/`fi`.

## Required reading inside the repo

In this order:

1. **`CLAUDE.md`** — repo layout, common commands, conventions, "What HAS
   landed" table. Updated after every milestone.
2. **`AGENTS.md`** — cross-tool conventions, "done" checklist, URL
   rewrite contract, Convex Dashboard embedding contract.
3. **`docs/ROADMAP.md`** — every milestone status. v0.6.1 is your
   target.
4. **`docs/V0_6_INSTALLER_PLAN.md`** — design + chunk-by-chunk landing
   log for v0.6. Anti-features explicitly called out.
5. **`.claude/skills/synapse-installer/SKILL.md`** — bash conventions,
   the 9 real-world bugs caught during v0.6.0, the canonical real-VPS
   smoke recipe.
6. **`docs/PRODUCTION.md`** — the operator-facing install guide. Keep
   it current as v0.6.1 commands ship.
7. **`docs/ARCHITECTURE.md`** — design decisions and what's deliberately
   out of scope.
8. **`docs/SCREENSHOTS.md`** — UI tour. Refresh captures when you
   change dashboard pages.

When in doubt, `git log --oneline | head -50` shows the journey. Commit
bodies are intentionally verbose — they explain trade-offs not restated
in the code.

## What's deliberately out of scope

- Stripe/Orb billing parity
- WorkOS/SAML (we have email+password JWT; OIDC is v1.0+)
- Multi-region / deployment classes
- Discord/Vercel integrations
- LaunchDarkly equivalent
- Rebuilding the Convex backend itself (the `defy-works/convex-backend`
  fork lives in a different layer; we orchestrate the upstream image
  unmodified)

If you're tempted to add one, move it to the roadmap and discuss.

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

- The operator gave the synapse-test VPS as your sandbox. Use it.
  Don't optimize for skipping smoke tests.
- v0.6.1 commands look small in isolation but compose into the
  operator-experience surface. Treat each as a product, not a script.
  The success screen / log format / failure-mode messaging matters as
  much as the happy path.
- Subagents are valuable for research-heavy work (the v0.6.0 chunks
  used Coolify/k3s/Tailscale comparative reads; the Convex Dashboard
  fix used two parallel agents to cross-verify the postMessage
  protocol). Lean on them when the question spans more than a single
  file or branches into "what does the upstream do?".
- The operator wants 5k stars eventually. The path there is "first
  30 seconds of using Synapse is the installer" + "every operator
  pain point in self-hosting Convex is something we already solved".
  Don't lose that frame.

Start by reading `CLAUDE.md`, `AGENTS.md`, `docs/ROADMAP.md`, and the
`synapse-installer` skill end-to-end. Then say hi to the operator in
pt-BR and confirm where to begin (`--upgrade` is the obvious first
v0.6.1 ticket, but check). BORAAAA MESTREEEEE!! vamos lá.
