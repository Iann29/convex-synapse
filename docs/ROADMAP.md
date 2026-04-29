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
- [ ] QUICKSTART verified end-to-end on a fresh machine

## v0.2 — "It's nice" ✅ MOSTLY DONE

- [x] Personal access tokens (`POST /v1/create_personal_access_token`) + dashboard `/me`
- [x] Health monitoring worker — reconciles `deployments.status` with Docker reality every 30s
- [x] Real Go test suite (68 test functions, ~7s, postgres testcontainer)
- [x] Async provisioning (returns 201 immediately; goroutine + 5min timeout + panic recovery + orphan-row sweep at startup)
- [x] Delete during provisioning is race-free (handler trusts the goroutine for cleanup)
- [ ] `npx convex` CLI compatibility (auth flow, deploy keys)
- [x] Reverse proxy mode so deployments don't need exposed host ports (`SYNAPSE_PROXY_ENABLED=true`)
- [ ] Auto-restart for `failed`/`stopped` deployments (manual recovery only in v0.2)
- [ ] Migration helper: import an existing standalone self-hosted deployment into Synapse
- [ ] Pagination on team / project listings (PAT list already paginated)

## v0.3 — "Looks the part"

UI redesign to match the Convex Cloud dashboard aesthetic. Tracked in
[docs/DESIGN.md](DESIGN.md). Will be developed on a feature branch by a
frontend-specialised agent and merged via PR (not direct to main).

- [ ] Top app bar (team picker + tabs + profile menu)
- [ ] Home page redesign (Projects / Deployments tabs, grid+list toggle, empty state)
- [ ] Team Settings shell (left sidebar + General / Members / Access Tokens panes)
- [ ] Avatar component with deterministic gradient + initials
- [ ] Logo + favicon

## v1.0 — "Safe to depend on"

- [ ] Audit log (subset of cloud's 66 events)
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
| Cloud backups | ❌ v1.0 |

The dashboard fork (when complete) covers data, functions, logs, schedules,
files, history, and per-deployment settings — all by talking directly to the
Convex backend with the admin key Synapse hands out, no extra work.
