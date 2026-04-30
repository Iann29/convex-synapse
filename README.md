# Synapse

[![CI](https://github.com/Iann29/convex-synapse/actions/workflows/ci.yml/badge.svg)](https://github.com/Iann29/convex-synapse/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.22%2B-00ADD8?logo=go)](https://go.dev/)
[![Next.js](https://img.shields.io/badge/next.js-16-000?logo=next.js)](https://nextjs.org/)

**Open-source control plane for self-hosted [Convex](https://www.convex.dev/) deployments.**

Synapse is the missing piece for self-hosted Convex: a management layer that lets you create teams, projects, and provision multiple Convex deployments from a single dashboard — replicating the experience of Convex Cloud (`dashboard.convex.dev`) on your own infrastructure.

## The Problem

Convex Cloud has a slick dashboard where you log in, see all your teams, projects, and deployments, and click around to spin new ones up. The whole orchestration layer behind it is called **Big Brain** — and it's proprietary, closed-source, and only runs on Convex's infrastructure.

The official self-hosted dashboard skips that entire experience. It connects to **one** backend instance via a hardcoded URL and admin key. No teams, no projects, no provisioning. Every team/project value is a stub with `id: 0`.

Synapse fills that gap.

## Architecture

```
        ┌────────────────────────┐
        │  Dashboard (Next.js)   │  ← fork of Convex Cloud dashboard
        │  port 6790             │
        └──────────┬─────────────┘
                   │ REST API (OpenAPI v1 compatible)
        ┌──────────▼─────────────┐
        │  Synapse (Go)          │  ← this repo
        │  port 8080             │
        │  • Postgres (metadata) │
        │  • Docker API          │
        └──────────┬─────────────┘
                   │ provisions
       ┌───────────┼───────────┐
       ▼           ▼           ▼
   ┌───────┐  ┌───────┐  ┌───────┐
   │ BE 1  │  │ BE 2  │  │ BE N  │  ← independent Convex backends
   │ :3210 │  │ :3211 │  │ :321N │
   └───────┘  └───────┘  └───────┘
```

## Status

**v0.5 (HA-per-deployment) feature-complete — 10/10 chunks landed.**
Operators can opt a deployment into HA at create time: the backend
provisions two replicas backed by Postgres + S3, the proxy fails over
between them on connection errors, the health worker tracks replica
state independently, and the dashboard surfaces a toggle + `HA ×2`
badge. Single-replica behavior is unchanged — HA is a per-deployment
switch behind `SYNAPSE_HA_ENABLED`.

The mechanical pieces deferred to v0.5.1: the real-backend
`docker kill`-the-active failover test (skeleton + compose profile
shipped, see [docs/HA_TESTING.md](docs/HA_TESTING.md)), and the
`upgrade_to_ha` worker that migrates an existing single-replica
deployment to HA via `snapshot_export`/`snapshot_import` (the endpoint
is reserved with full validation; today returns 501).

The dashboard matches the Convex Cloud aesthetic (top app bar, team
picker, redesigned home, team-settings shell), and the control plane is
multi-node-safe (v0.3). Paginated team/project listings and a migration
helper for existing self-hosted backends are also live.

A `docker compose up -d` plus a register call gets you a control-plane API,
a dashboard, and the ability to provision real Convex backend containers in
about a second per deployment. Multiple Synapse processes can share one
Postgres + one Docker daemon — resource allocators retry on conflict,
periodic workers coordinate via Postgres advisory locks, and async
provisioning is a persistent queue with parallel
`SELECT FOR UPDATE SKIP LOCKED` consumers.

What works today:
- Email+password auth (JWT) and `syn_*` personal access tokens (hashed at rest)
- Multi-team membership via opaque invite tokens (`/v1/team_invites/accept`)
- Projects: CRUD, rename, delete, default env vars (set/delete batch)
- Deployments: real Convex backend container per deployment, ~1s provisioning
- **Adopt existing**: register a Convex backend running outside Synapse via
  `POST /v1/projects/{id}/adopt_deployment` (probes `/version` +
  `/api/check_admin_key` before persisting; UI dialog on the project page)
- `npx convex` CLI compatibility (signed admin keys + `cli_credentials` endpoint)
- Reverse proxy mode (`/d/{name}/*`) so deployments share a single host port
- Health worker that reconciles `deployments.status` with Docker reality
  (skips adopted rows — Synapse never touches external containers)
- Optional auto-restart for stopped containers
- Audit log (Cloud-vocabulary actions: `createTeam`, `createProject`,
  `adoptDeployment`, …)
- **Paginated listings** on `/v1/teams`, `/v1/teams/{ref}/list_projects`,
  `list_members`, `list_deployments`, `/v1/projects/{id}/list_deployments` —
  `?limit&?cursor` + `X-Next-Cursor` header (response stays a bare array)
- Redesigned dashboard: top app bar with team picker, home with
  Projects/Deployments tabs, team-settings shell with sidebar, deterministic
  avatar gradients
- **HA-per-deployment** (opt-in via `SYNAPSE_HA_ENABLED=true` +
  `SYNAPSE_STORAGE_KEY`): `ha:true` on `create_deployment` provisions 2
  replicas backed by Postgres + S3 with AES-GCM-encrypted creds at rest,
  proxy fails over between replicas on connection error, health worker
  rolls up replica statuses into the deployment-level status, dashboard
  toggle + `HA ×N` badge. `POST /v1/deployments/{name}/upgrade_to_ha`
  endpoint reserved (validation live; worker mechanics in v0.5.1). See
  [docs/HA_TESTING.md](docs/HA_TESTING.md) for the operator setup.
- ~131 Go integration tests + 20 Playwright e2e green in CI

See [docs/ROADMAP.md](docs/ROADMAP.md) for what's next (the v0.5.1
follow-up — `upgrade_to_ha` worker + real-backend failover e2e), the
v0.5 design in [docs/V0_5_PLAN.md](docs/V0_5_PLAN.md), and the operator
guide in [docs/HA_TESTING.md](docs/HA_TESTING.md). What's deliberately
out of scope is documented in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Repo layout

| Path | Purpose |
|---|---|
| `synapse/` | Go backend — the control plane (REST API + provisioner) |
| `dashboard/` | Next.js frontend — original app talking to Synapse's REST surface |
| `docs/` | Architecture, quickstart, roadmap, API ref, v0.5 plan, HA testing guide |
| `docker-compose.yml` | One-command local stack (+ optional `ha` profile for HA testing) |

## Quickstart

```bash
git clone https://github.com/Iann29/convex-synapse.git
cd convex-synapse
cp .env.example .env
echo "SYNAPSE_JWT_SECRET=$(openssl rand -hex 64)" >> .env
docker compose up -d
```

Open `http://localhost:6790`, register, create a team → project → deployment.
Synapse provisions a fresh Convex backend container in about a second.

For details (manual dev path, curl examples, `npx convex` integration), see
[docs/QUICKSTART.md](docs/QUICKSTART.md).

## Tests

```bash
# Go integration tests (need a postgres at localhost:5432, or set
# SYNAPSE_TEST_DB_URL). Each test gets its own isolated DB.
cd synapse && go test ./... -count=1

# Playwright end-to-end against the live compose stack
cd dashboard
npm install
npx playwright install chromium
npm run test:e2e
```

The CI pipeline runs all four jobs (Go, Next.js build, compose build, full
Playwright suite) on every push.

## License

Apache License 2.0 — see [LICENSE](LICENSE).

The dashboard component (`dashboard/`) is an original Next.js app that
talks to Synapse's REST surface; it is not a fork of any Convex code, and
also ships under Apache 2.0. (Reading the Convex Cloud dashboard
[OpenAPI spec](https://github.com/get-convex/convex-backend/blob/main/npm-packages/dashboard/dashboard-management-openapi.json)
to design a compatible API is fair use; we ship no code from that repo.)

## Why "Synapse"?

A synapse is the connection between neurons. Big Brain is the neuron — Synapse is what wires the deployments together into something coherent. Also, it's short.
