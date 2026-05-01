# Synapse

[![CI](https://github.com/Iann29/convex-synapse/actions/workflows/ci.yml/badge.svg)](https://github.com/Iann29/convex-synapse/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.22%2B-00ADD8?logo=go)](https://go.dev/)
[![Next.js](https://img.shields.io/badge/next.js-16-000?logo=next.js)](https://nextjs.org/)

**Open-source control plane for self-hosted [Convex](https://www.convex.dev/) deployments.**

Convex's official self-hosted backend is great — but their dashboard
only talks to one instance via a hardcoded admin key. No teams, no
projects, no provisioning. Synapse is the management layer that fills
that gap: teams, projects, multi-deployment, audit log, `npx convex`
auth, and an embedded Convex Dashboard with one-click "Open dashboard".

```bash
curl -sSf https://raw.githubusercontent.com/Iann29/convex-synapse/main/setup.sh \
  | bash -s -- --domain=synapse.yourdomain.com
```

Three minutes later: stack up on your VPS, TLS via Caddy + Let's
Encrypt, admin user registered, demo Convex deployment provisioned and
self-tested. Validated end-to-end against a real Hetzner CPX22.

![Project page with a deployment provisioned](docs/screenshots/04-project-deployment.png)

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  Synapse Dashboard          Next.js • teams/projects/audit  │
│                             port 6790                       │
└──────────────┬──────────────────────────────────────────────┘
               │ REST API (OpenAPI v1 compatible)
┌──────────────▼──────────────────────────────────────────────┐
│  Synapse API                Go • chi • pgx • docker SDK     │
│                             port 8080                       │
│                             • Postgres (metadata)           │
│                             • /d/{name}/* reverse proxy     │
└──────────────┬──────────────────────────────────────────────┘
               │ docker run
       ┌───────┼───────┐
       ▼       ▼       ▼
   ┌───────────────────────┐    ┌───────────────────────┐
   │ Convex backend × N    │    │ Convex Dashboard      │
   │ (one container per    │    │ (data/functions/logs  │
   │  deployment, ~1s spin │    │  UI, embedded under   │
   │  up; 2× replicas in   │    │  /embed/<name>)       │
   │  HA mode)             │    │                       │
   └───────────────────────┘    └───────────────────────┘
```

## What works

| Feature | Notes |
|---|---|
| Auth | email + password, JWT for the dashboard, opaque PATs for CLI / CI |
| Teams + invites | multi-user via opaque tokens, admin / member roles |
| Projects | CRUD, rename, delete, default env vars (batch) |
| Deployments | one Convex backend container per deployment, ~1 s provisioning |
| Adopt existing | register an external Convex backend without spinning a new one |
| `npx convex` CLI | signed admin keys + `cli_credentials` panel, paste-and-go |
| Reverse proxy | `/d/{name}/*` routing with multi-replica failover for HA |
| Convex Dashboard | hosted alongside Synapse, auto-logged via postMessage handshake |
| HA-per-deployment | opt-in: 2 replicas + external Postgres + S3, AES-GCM encrypted creds |
| Audit log | Cloud-vocabulary action names, admin-only read |
| Multi-node hygiene | retry-on-conflict, advisory-lock workers, `SELECT FOR UPDATE SKIP LOCKED` queue |
| Auto-installer | `./setup.sh` brings up the whole stack on a fresh VPS in ~3 min |
| Pagination | `?limit&?cursor` + `X-Next-Cursor` on every list endpoint |

**Tests:** ~136 Go integration tests + 20 Playwright e2e + 211 bats unit
tests, all green in CI on every push.

For roadmap, design notes, and what's deliberately out of scope, see
[`docs/ROADMAP.md`](docs/ROADMAP.md) and
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md). For more screenshots,
see [`docs/SCREENSHOTS.md`](docs/SCREENSHOTS.md).

## Quickstart

### Production VPS — with TLS (one-liner)

```bash
curl -sSf https://raw.githubusercontent.com/Iann29/convex-synapse/main/setup.sh \
  | bash -s -- --domain=synapse.yourdomain.com
```

DNS A-record must already point at the VPS. Caddy handles TLS via
Let's Encrypt automatically. The script clones the repo into
`/tmp/convex-synapse-bootstrap-<pid>` and re-execs itself from there
— operators see "Bootstrapping Synapse installer from ..." on stderr
before the real install starts.

### Local dev — no TLS, no domain

```bash
curl -sSf https://raw.githubusercontent.com/Iann29/convex-synapse/main/setup.sh \
  | bash -s -- --no-tls --skip-dns-check --non-interactive
```

Open `http://localhost:6790`, register, click around. Useful flags
worth knowing: `--enable-ha`, `--doctor`, `--install-dir=`,
`--upgrade` (v0.6.1), `--no-bootstrap` (run from a local checkout).

### Manual install (inspect first)

If you'd rather review the script before running it (good practice
for serious deploys), `git clone` and run it locally — same script,
same flags:

```bash
git clone https://github.com/Iann29/convex-synapse.git
cd convex-synapse && ./setup.sh --domain=synapse.yourdomain.com
```

For everything else (custom Caddy/nginx, HA cluster setup, the
`npx convex` flow), see
[`docs/PRODUCTION.md`](docs/PRODUCTION.md) /
[`docs/QUICKSTART.md`](docs/QUICKSTART.md) /
[`docs/HA_TESTING.md`](docs/HA_TESTING.md).

## Repo layout

| Path | Purpose |
|---|---|
| `synapse/` | Go control plane — REST API + provisioner + reverse proxy |
| `dashboard/` | Next.js dashboard talking to the REST surface |
| `setup.sh` + `installer/` | Pure-bash auto-installer + bats tests |
| `docs/` | Architecture, roadmap, production guide, design notes |
| `docker-compose.yml` | Local stack + optional `ha` / `caddy` profiles |

## Tests

```bash
# Go integration (postgres on :5432 or set SYNAPSE_TEST_DB_URL)
cd synapse && go test ./... -count=1

# Playwright e2e against the live compose stack
cd dashboard && npm ci && npx playwright install chromium && npm run test:e2e

# Bats — installer + setup.sh
docker run --rm -v "$PWD:/code" -w /code synapse-bats -r installer/test/
```

CI runs all four (Go, Next.js, compose build, Playwright) plus the
installer suite on every push.

## License

Apache License 2.0 — see [LICENSE](LICENSE).

The dashboard component is an original Next.js app talking to
Synapse's REST surface, not a fork of any Convex code. Reading the
Convex Cloud dashboard
[OpenAPI spec](https://github.com/get-convex/convex-backend/blob/main/npm-packages/dashboard/dashboard-management-openapi.json)
to design a compatible API is fair use; we ship no code from that
repo.

---

> Why "Synapse"? A synapse is the connection between neurons. Big
> Brain is the closed-source neuron Convex Cloud uses; Synapse is what
> wires self-hosted deployments together into something coherent.
> Also, it's short.
