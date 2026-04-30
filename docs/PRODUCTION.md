# Running Synapse on a VPS

End-to-end guide for taking the local `docker compose up -d` stack and
turning it into something that survives a real domain, real users, and a
firewall. Geared at a single-VPS setup; multi-host / k8s is in
[ROADMAP.md](ROADMAP.md) (v1.0).

This doc covers **single-replica** deployments end-to-end (the
boring-but-works case). HA-per-deployment has a separate setup
(Postgres + S3) — see [HA_TESTING.md](HA_TESTING.md).

## What you'll need

| Thing | Why |
|---|---|
| A VPS with **2+ GB RAM, 2+ vCPU, 20+ GB disk** | Each Convex backend container is light, but image + Postgres + dashboard + a couple of deployments adds up |
| Docker + Docker Compose v2 | The whole stack runs in containers |
| A **domain** pointing at the VPS (`A` or `AAAA` record) | TLS termination + a stable URL for `npx convex`. Subdomain works fine, e.g. `synapse.yourdomain.com` |
| Outbound internet from the VPS | First-time pull of `ghcr.io/get-convex/convex-backend` |
| Caddy / Cloudflare / nginx (pick one) | TLS in front of port 8080. Caddy is the simplest — config below |

## Architecture, abridged

```
[laptop] --> https://synapse.yourdomain.com/...  (Caddy on :443)
                                                         │ proxies to
                                                         ▼
                                       [VPS] :8080  Synapse API + /d/{name}/* proxy
                                                         │
                                       Convex backend container(s)
                                       on the synapse-network bridge
                                       (NOT exposed to the internet)
```

Synapse's reverse proxy mounts `/d/{name}/*` on the same `:8080` listener
as the API, so a single TLS-terminating front-end protects everything.
Per-deployment ports stay internal — your firewall only opens 80/443.

## 1. Provision the VPS

DigitalOcean / Hetzner / Linode / OVH — anything with Docker support
works. After SSH'ing in:

```bash
# Debian / Ubuntu
sudo apt-get update
sudo apt-get install -y docker.io docker-compose-v2 git

# (Optional) run docker without sudo
sudo usermod -aG docker $USER && newgrp docker
```

## 2. Clone Synapse

```bash
git clone https://github.com/Iann29/convex-synapse.git
cd convex-synapse
cp .env.example .env
```

## 3. Edit `.env` for production

The `.env` file shipped with the repo is dev-friendly — change these
**before** running `docker compose up`:

```bash
# Auth — generate a real secret (NEVER commit this)
SYNAPSE_JWT_SECRET=<output of `openssl rand -hex 64`>

# Your VPS's public origin. Without this, `npx convex` from your laptop
# gets a "http://127.0.0.1:3210" URL that points at the VPS's loopback,
# which obviously won't work.
SYNAPSE_PUBLIC_URL=https://synapse.yourdomain.com

# Reverse-proxy mode: route /d/{name}/* to the right backend container.
# With PUBLIC_URL set, this gives you ONE public port (8080) for
# everything — no need to expose the per-deployment 3210-3500 range.
SYNAPSE_PROXY_ENABLED=true

# Postgres password — at minimum, change from the default `synapse:synapse`
POSTGRES_USER=synapse
POSTGRES_PASSWORD=<strong random>

# CORS — restrict to your dashboard origin once you go behind TLS
SYNAPSE_ALLOWED_ORIGINS=https://synapse.yourdomain.com

# Healthcheck-via-network: required when synapse runs in a container
# (which it does, in compose). Don't change unless you know why.
SYNAPSE_HEALTHCHECK_VIA_NETWORK=true
```

The `SYNAPSE_DB_URL` line in `.env` already references the compose
network's `postgres` service — leave it.

## 4. Bring up the stack

```bash
docker compose up -d
docker compose logs -f synapse  # confirm "synapse starting"
```

The first run pulls the dashboard + synapse images and the first
deployment-create pulls `ghcr.io/get-convex/convex-backend:latest` —
budget a couple of minutes the first time.

## 5. TLS termination with Caddy

Caddy is the quickest path to HTTPS — auto-renews Let's Encrypt certs,
no fiddling. Install on the host:

```bash
sudo apt-get install -y caddy
```

Edit `/etc/caddy/Caddyfile`:

```caddy
synapse.yourdomain.com {
    # /api → the dashboard (port 6790)
    @dashboard {
        not path /v1/* /d/* /health
    }
    handle @dashboard {
        reverse_proxy localhost:6790
    }

    # /v1/* (API), /d/{name}/* (proxy), /health → Synapse
    handle {
        reverse_proxy localhost:8080
    }
}
```

Reload Caddy:

```bash
sudo systemctl reload caddy
```

DNS `A` record (`synapse.yourdomain.com` → VPS IP) needs to be in place
before this — Caddy negotiates the cert on the first request.

## 6. Firewall

Only open the public TLS ports:

```bash
sudo ufw allow 22       # SSH (already open, just in case)
sudo ufw allow 80/tcp   # Caddy redirects HTTP → HTTPS
sudo ufw allow 443/tcp  # the actual API
sudo ufw enable
```

Postgres (5432), Synapse (8080), dashboard (6790), and the Convex
backend host-port range (3210-3500) all stay private — Caddy is the
only thing that talks to them.

## 7. Smoke test

From your laptop:

```bash
# 1. Register the first user
curl -sf -X POST https://synapse.yourdomain.com/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@yourdomain.com","password":"strong-random-here","name":"You"}'

# 2. Visit the dashboard
open https://synapse.yourdomain.com    # browser
```

Then in the dashboard: create a team → project → deployment. Wait for
the row to flip to `running` (~1 second).

Click **CLI credentials** on the deployment row — the export snippet
should now reference `https://synapse.yourdomain.com/d/<name>`, NOT
`http://127.0.0.1:<port>`. Paste the snippet into a local shell and
`npx convex dev --once` should connect from anywhere.

## What about HA?

Single-replica deployments survive everything except the VPS itself
dying — if the host reboots, Synapse comes back up and the health
worker reconciles. If the host is permanently lost, you've lost the
deployment.

For HA-per-deployment (active-passive replicas backed by a separate
Postgres + S3, with proxy failover), see [HA_TESTING.md](HA_TESTING.md).
The compose stack has an opt-in `ha` profile that brings up MinIO + a
backend Postgres alongside, but for production you'd typically point
`SYNAPSE_BACKEND_*` at a managed Postgres (Neon, Supabase, RDS) and
managed S3 (R2, B2, AWS).

## Backups

Synapse's metadata DB (teams, projects, deployment registry) lives in
the `synapse-pgdata` Docker volume. Standard Postgres backup story
applies:

```bash
# Daily backup via a cron job
docker compose exec -T postgres \
  pg_dump -U synapse synapse | gzip > /backups/synapse-$(date +%F).sql.gz
```

For the Convex backend data itself: the SQLite file lives in
`synapse-data-<deployment>` per-deployment volume. Snapshot the volume
or use `npx convex export` against each deployment if you need
portable dumps.

## Upgrading Synapse

```bash
cd convex-synapse
git pull
docker compose build synapse dashboard
docker compose up -d synapse dashboard
```

Embedded migrations apply on startup — operator never runs them
manually. The provisioner queue + advisory locks keep an in-flight
deploy from being lost during the restart.

## Common gotchas

- **`npx convex` says "could not connect"** — `SYNAPSE_PUBLIC_URL` is
  unset or wrong. The CLI got a `http://127.0.0.1:<port>` URL that
  only resolves on the VPS itself.
- **502 Bad Gateway from Caddy** — Synapse-API container is down.
  `docker compose logs synapse` will tell you why (usually a missing
  env var on first boot).
- **CORS error in browser DevTools** — `SYNAPSE_ALLOWED_ORIGINS` is
  set to `*` by default. If you locked it down, make sure your
  dashboard origin (the one in the URL bar) is in the allowed list.
- **Disk filling up** — old Convex backend images and volumes
  accumulate. `docker system prune` cleans dangling stuff;
  `docker volume ls -q --filter name=synapse-data-` shows
  per-deployment volumes (don't blanket-delete — these have user data).
- **Postgres volume can't be backed up while running** —
  `pg_dump` is the right tool, not a raw volume copy. The compose
  Postgres exposes `:5432` only on the docker network; use `docker
  compose exec` to reach it.

## What's NOT included today

- Auto-renewing TLS for **per-deployment** custom domains (v1.0 — Caddy
  only terminates Synapse's own domain right now)
- Multi-region replication (out of scope; lease design forbids it
  upstream — see V0_5_PLAN.md)
- Automatic Postgres backups (manual `pg_dump` + cron is the v0.5 story)
- A K8s / Helm install path (v1.0)
