# Roadmap

Checklist-style. Items tracked in TaskList during active dev sessions.

## v0.1 — "It runs"

- [x] Repo bootstrapped (git, README, structure)
- [ ] Go backend boilerplate: chi, slog, /health
- [ ] Postgres schema + migrations
- [ ] Auth: register, login, JWT middleware, /v1/me
- [ ] Teams API: create, list, get, members
- [ ] Projects API: create, list, get, env vars
- [ ] Docker provisioner service
- [ ] Deployments API: create (with real container provisioning), list, get, delete
- [ ] Dashboard fork imported, builds locally
- [ ] docker-compose.yml: postgres + synapse + dashboard up
- [ ] QUICKSTART that gets a new user from zero to a running deployment in 5 minutes

## v0.2 — "It's nice"

- [ ] Personal access tokens (for `npx convex` CLI)
- [ ] Project env vars CRUD
- [ ] Deploy keys
- [ ] Reverse proxy mode (so backends don't need exposed ports)
- [ ] Health monitoring of provisioned backends (auto-restart, status reporting)
- [ ] Migration helper: import an existing self-hosted deployment into Synapse

## v1.0 — "It's safe to depend on"

- [ ] Audit log (subset)
- [ ] Custom domains with auto-TLS
- [ ] Backups (volume snapshots → S3)
- [ ] RBAC: project-level roles
- [ ] OAuth/SSO (probably via Authentik/Zitadel integration)
- [ ] Kubernetes provisioner (alternative to Docker)
- [ ] Helm chart

## Maybe never

- Full Stripe/Orb billing parity (irrelevant for self-hosted)
- LaunchDarkly equivalent (use a static config)
- WorkOS-specific paths (use a generic OIDC provider instead)
