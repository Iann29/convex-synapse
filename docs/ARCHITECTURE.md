# Architecture

## What we're replacing

Convex Cloud has a service called **Big Brain** that the dashboard talks to. Big Brain is closed-source. It exposes a stable v1 management API documented at `npm-packages/dashboard/dashboard-management-openapi.json` in the open-source [convex-backend](https://github.com/get-convex/convex-backend) repo.

Synapse implements (a subset of) that API. The OpenAPI spec is our north star — anything that talks to Big Brain should be able to talk to Synapse with minimal changes.

## What's in / what's out

### In scope (v0)

| Resource | Operations |
|---|---|
| Auth | Email+password register/login, JWT sessions, personal access tokens |
| Teams | Create, list, get, list members, invite member |
| Projects | Create, list (per team), get, delete, env vars |
| Deployments | Create (provisions a Docker container), list, get, delete, deploy keys |
| Provisioning | Docker Engine API — pulls `ghcr.io/get-convex/convex-backend`, allocates a port, generates admin key |

### Out of scope (v0, maybe v1+)

- WorkOS / SAML / SSO
- Stripe / Orb billing & invoices
- Audit logs (66 distinct event types in Cloud)
- Cloud backups (S3 streaming)
- Custom domains with Let's Encrypt automation
- Preview deployments
- Multi-region (we assume one host)
- Deployment classes (s16/s256/d1024 — everything is one tier)
- LaunchDarkly feature flags
- Discord/Vercel integrations
- Usage analytics / Databricks queries

These are explicitly stubbed in the dashboard fork (entitlements report everything as enabled, billing endpoints return empty).

## Components

### Synapse (Go backend)

- **Router**: `chi` — small, idiomatic, no magic
- **DB**: Postgres via `pgx/v5`. Migrations with `golang-migrate`.
- **Auth**: `golang-jwt/jwt/v5` for JWTs. `bcrypt` for passwords. Personal access tokens stored as `Argon2id` hashes (faster verification under load).
- **Provisioner**: `docker/docker` client library. Each deployment = one container. Ports allocated from a configurable pool (default `3210-3500`).
- **Config**: env-based, `godotenv` for local dev.
- **Logging**: `slog` with structured JSON output.

### Dashboard (Next.js fork)

- Cloned from `npm-packages/dashboard/` of `get-convex/convex-backend`
- Repointed: `NEXT_PUBLIC_BIG_BRAIN_URL` → Synapse
- Auth swap: WorkOS → email+password against Synapse
- Stubbed: WorkOS hooks, Stripe forms, audit log pages, SSO config

### Postgres schema

Tables:
- `users` — id, email (unique), password_hash, name, created_at
- `teams` — id, name, slug (unique), creator_user_id, created_at
- `team_members` — team_id, user_id, role (`admin`/`member`), created_at
- `projects` — id, team_id, name, slug (unique per team), created_at
- `deployments` — id, project_id, name (unique global), deployment_type (`dev`/`prod`/`preview`), status, container_id, host_port, admin_key, instance_secret, created_at
- `project_env_vars` — id, project_id, name, value, deployment_types (text[])
- `access_tokens` — id, user_id, scope (team/project/deployment), scope_id, token_hash, name, last_used_at, created_at, expires_at
- `audit_events` — (placeholder, not used in v0)

### Provisioning flow

```
POST /v1/projects/{id}/create_deployment
  ↓
1. Validate caller has admin on project's team
2. Generate deployment name: friendly-animal-1234
3. Generate instance_secret (32 bytes) and admin_key
4. Pick a free port from the pool (DB-locked allocation)
5. docker run ghcr.io/get-convex/convex-backend:latest
   --name convex-{name}
   -p {port}:3210
   -e INSTANCE_NAME={name}
   -e INSTANCE_SECRET={secret}
   -v synapse_data_{name}:/convex/data
   --network synapse-network
6. Wait for /api/check_admin_key to return 200
7. INSERT INTO deployments (...)
8. Return PlatformDeploymentResponse
```

### Auth flow (dashboard ↔ Synapse ↔ deployment)

```
1. User logs in via dashboard (email+password)
   → Synapse returns short-lived JWT (15min) + refresh token
2. Dashboard calls /v1/projects/{id}/deployment?reference=...
   → Synapse responds with deployment metadata + deploymentUrl
3. Dashboard calls Synapse /v1/deployments/{name}/auth
   → Synapse returns adminKey (already in DB)
4. Dashboard talks DIRECTLY to the Convex backend at deploymentUrl
   using adminKey (just like cloud dashboard does)
```

The dashboard uses Synapse only for management — the actual data/function/log views go straight to the backend deployment.

## Networking

Single Docker network: `synapse-network`. Synapse and all provisioned backends share it. The dashboard runs on the host network (or in the same Docker network if compose-orchestrated). Backends are exposed:
- Internally: by container name (`convex-{name}:3210`)
- Externally: through Synapse's reverse proxy at `/d/{name}/...` OR via host-port mapping

For v0 we go with **host-port mapping** — simpler, easier to debug. Reverse proxy is a v1 thing.

## Why Go?

Three reasons:
1. Single binary, no runtime — easy to ship
2. Solid Docker SDK
3. Concurrency primitives match the workload (lots of "wait for container to be healthy" + "watch port")

Could have been Rust (matches the Convex backend stack), but Go is faster to ship and the user's preference.

## Compatibility goals

- **OpenAPI v1**: aim for >80% endpoint coverage of the stable Convex management API. Tools that use `@convex-dev/platform` should work against Synapse with only the base URL changed.
- **Dashboard**: target a vanilla cloud dashboard build with our env vars. Diffs from upstream tracked in `dashboard/PATCHES.md`.
- **CLI**: `npx convex` should be able to talk to Synapse for `dev`, `deploy`, `logs`, etc. (post-v0 goal).
