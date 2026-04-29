# Synapse

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

🚧 **Early development.** See [docs/ROADMAP.md](docs/ROADMAP.md) for the build plan and current status.

## Repo layout

| Path | Purpose |
|---|---|
| `synapse/` | Go backend — the control plane (REST API + provisioner) |
| `dashboard/` | Next.js frontend — fork of the Convex Cloud dashboard, repointed at Synapse |
| `docs/` | Architecture notes, quickstart, roadmap |
| `docker-compose.yml` | One-command local stack |

## Quickstart

See [docs/QUICKSTART.md](docs/QUICKSTART.md) once it lands. For now:

```bash
# Coming soon
docker compose up -d
```

## License

TBD (likely Apache-2.0 or MIT). The forked dashboard inherits Convex's [FSL-1.1-Apache-2.0](https://github.com/get-convex/convex-backend/blob/main/LICENSE) license; consult that for usage of the dashboard portion.

## Why "Synapse"?

A synapse is the connection between neurons. Big Brain is the neuron — Synapse is what wires the deployments together into something coherent. Also, it's short.
