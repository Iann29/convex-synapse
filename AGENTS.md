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
  internal/db/          pgx pool, migrations, retry/advisory-lock helpers
  internal/docker/      Docker SDK + Provision/Destroy/Restart/GenerateAdminKey
  internal/health/      Periodic reconciler worker
  internal/middleware/  chi middleware (auth, logging, CORS)
  internal/provisioner/ Persistent job queue + parallel worker
  internal/proxy/       Optional /d/{name}/* reverse proxy
  internal/test/        Integration test harness (package synapsetest)

dashboard/              Next.js 16 + Tailwind 4 + Playwright
docs/                   Architecture, roadmap, design notes, quickstart, API ref
docker-compose.yml      Local stack
```

Read `docs/ARCHITECTURE.md` and `docs/DESIGN.md` end-to-end before making
non-trivial changes — the trade-offs are explained there and not always
re-stated in the code.

## Multi-node ground rules

The codebase is meant to run with N processes against one Postgres + one
Docker daemon. Three patterns to follow when you write new code:

- **Resource allocators** (anything that does "SELECT to find a free X, then
  INSERT it"): wrap in `db.WithRetryOnUniqueViolation`. UNIQUE catches the
  race; retry generates a fresh candidate.
- **Periodic workers**: wrap each tick in `db.WithTryAdvisoryLock(pool, key, fn)`.
  Single-node always acquires; multi-node coordinates.
- **Long async work**: enqueue a row, run a worker with
  `SELECT FOR UPDATE SKIP LOCKED` + parallel goroutines. Don't spawn a
  per-handler goroutine.

See `internal/provisioner/` for the canonical example of all three.

## What "done" looks like

A feature is done when:

- [ ] `cd synapse && go build ./... && go vet ./... && go test ./... -count=1` is clean
- [ ] The endpoint is reachable on a freshly-truncated DB
- [ ] Auth/authz checks (member-only, admin-only) are exercised
- [ ] Errors return structured JSON via `writeError(...)` with stable codes
- [ ] **Integration test** added in `synapse/internal/test/<resource>_test.go`
- [ ] **Playwright spec** added in `dashboard/tests/` if user-facing
- [ ] **Audit hook** added (`audit.Record(...)`) on every mutating success path
- [ ] `docs/API.md` updated for any new/changed endpoint
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
