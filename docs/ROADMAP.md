# Roadmap

## v0.1 — "It runs end-to-end" ✅ MOSTLY DONE

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
- [x] Playwright e2e tests through the full compose stack (7 tests, ~30s)
- [x] Dashboard delete-deployment action
- [x] CORS middleware
- [x] CI: Go build/vet/test + Next.js build + compose build + Playwright e2e
- [ ] Dashboard polish: loading skeletons, deployment status updates while provisioning
- [ ] QUICKSTART verified end-to-end on a fresh machine

## v0.2 — "It's nice"

- [ ] Personal access tokens (`POST /v1/create_personal_access_token`)
- [ ] `npx convex` CLI compatibility (auth flow, deploy keys)
- [ ] Reverse proxy mode so deployments don't need exposed host ports
- [ ] Health monitoring of provisioned backends (auto-restart, status reporting in dashboard)
- [ ] Migration helper: import an existing standalone self-hosted deployment into Synapse
- [ ] Real Go test suite (testcontainers for postgres, fake docker daemon)
- [ ] Dashboard: rename/delete projects, manage env vars
- [ ] Pagination on listings

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
| Personal access tokens | 🚧 v0.2 |
| Cloud backups | ❌ v1.0 |

The dashboard fork (when complete) covers data, functions, logs, schedules,
files, history, and per-deployment settings — all by talking directly to the
Convex backend with the admin key Synapse hands out, no extra work.
