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

**v0.2 — feature-complete for daily use.** A `docker compose up -d` plus a
register call gets you a control-plane API, a dashboard, and the ability to
provision real Convex backend containers in about a second per deployment.

What works today:
- Email + password auth (JWT sessions)
- Personal access tokens for CLI / CI (`syn_*` opaque, hashed at rest)
- Multi-team membership via opaque invite tokens (`/v1/team_invites/accept`)
- Project rename/delete, default env vars (set/delete batch)
- Real Convex backend container per deployment, ~1s provisioning
- Deployment delete tears down container + data volume idempotently
- Health worker reconciles `deployments.status` with Docker reality every 30s
- 55+ automated tests (Go integration + Playwright e2e) green in CI

See [docs/ROADMAP.md](docs/ROADMAP.md) for what's coming in v0.2/v1.0
and what's deliberately out of scope.

## Repo layout

| Path | Purpose |
|---|---|
| `synapse/` | Go backend — the control plane (REST API + provisioner) |
| `dashboard/` | Next.js frontend — fork of the Convex Cloud dashboard, repointed at Synapse |
| `docs/` | Architecture notes, quickstart, roadmap |
| `docker-compose.yml` | One-command local stack |

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
# Go unit tests
cd synapse && go test ./...

# Playwright end-to-end against the live compose stack
cd dashboard
npm install
npx playwright install chromium
npm run test:e2e
```

The CI pipeline runs all three (Go, Next.js build, full Playwright suite) on
every push.

## License

Apache License 2.0 — see [LICENSE](LICENSE).

The dashboard component (`dashboard/`) is an original Next.js app that
talks to Synapse's REST surface; it is not a fork of any Convex code, and
also ships under Apache 2.0. (Reading the Convex Cloud dashboard
[OpenAPI spec](https://github.com/get-convex/convex-backend/blob/main/npm-packages/dashboard/dashboard-management-openapi.json)
to design a compatible API is fair use; we ship no code from that repo.)

## Why "Synapse"?

A synapse is the connection between neurons. Big Brain is the neuron — Synapse is what wires the deployments together into something coherent. Also, it's short.
