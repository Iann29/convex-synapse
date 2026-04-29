# AGENTS.md

Conventions for AI coding agents (Claude Code, Codex, Cursor, Aider, etc.)
contributing to this repository. The goal is a consistent commit history and
a low-friction onboarding for the next agent that picks up the work.

For Claude Code-specific guidance see [CLAUDE.md](CLAUDE.md). This file is
the cross-tool subset.

## Ground rules

1. **Build green before commit.** `cd synapse && go build ./... && go vet ./...`
   must succeed.
2. **One feature per commit.** Mixing a refactor with a bug fix makes the
   history less useful. Follow the conventional-commit scopes already in `git log`.
3. **End-to-end test each slice.** Run the server, hit the new endpoints with
   `curl` (or a tiny script), confirm the happy path *and* the obvious 403/404/
   409. Capture the flow in the commit message body.
4. **Write the WHY in comments.** "What" is in the code; "why" decays into
   tribal knowledge if it isn't pinned next to the line that depends on it.
5. **Never widen scope silently.** If a small task forces a refactor, do
   the refactor in its own commit first.

## Repo orientation

```
synapse/              Go backend (chi, pgx, docker SDK)
dashboard/            Next.js dashboard fork (placeholder for now)
docs/                 Architecture, roadmap, quickstart
docker-compose.yml    Local stack
```

Read `docs/ARCHITECTURE.md` end-to-end before making non-trivial changes;
the trade-offs are explained there and not always re-stated in the code.

## What "done" looks like

A feature is done when:

- [ ] `cd synapse && go build ./... && go vet ./... && go test ./...` is clean
- [ ] The endpoint is reachable on a freshly-truncated DB
- [ ] The auth/authz checks (member-only, admin-only) are exercised
- [ ] Errors return structured JSON via `writeError`
- [ ] **Integration test added** in `synapse/internal/test/<resource>_test.go`
  if it's a new endpoint, OR a new test case if it's a behavior change
- [ ] **Playwright spec added** in `dashboard/tests/` if it's a new user-facing flow
- [ ] `docs/API.md` updated for any new/changed endpoint
- [ ] The commit message body lists the curl flow you actually ran
- [ ] `docs/ROADMAP.md` is updated if the change crosses a phase boundary

## What's intentionally out of scope

We aim for ~80% of Convex Cloud's stable v1 OpenAPI surface. We will not
implement: Stripe/Orb billing, WorkOS/SAML, audit-log writers, custom
domains with auto-TLS (v0), backups (v0), multi-region. See `docs/ROADMAP.md`.

When in doubt: simpler beats complete.
