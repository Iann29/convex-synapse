# Running Synapse on a VPS

End-to-end guide for taking a fresh Linux VPS to a running Synapse with
TLS, Caddy, a registered admin user, and a self-tested Convex
deployment. Geared at a single-VPS setup; multi-host / k8s is in
[ROADMAP.md](ROADMAP.md) (v1.0).

This doc covers **single-replica** deployments end-to-end (the
boring-but-works case). HA-per-deployment has a separate setup
(Postgres + S3) — see [HA_TESTING.md](HA_TESTING.md).

## What you'll need

| Thing | Why |
|---|---|
| A VPS with **2+ GB RAM, 2+ vCPU, 20+ GB disk** | Each Convex backend container is light, but image + Postgres + dashboard + a couple of deployments adds up. The `setup.sh` preflight refuses to proceed below this. |
| Linux (Debian/Ubuntu, Fedora/RHEL family, or Alpine) | Anything else falls outside the auto-installer's scope today |
| A **domain** pointing at the VPS (`A` or `AAAA` record) | TLS termination + a stable URL for `npx convex`. Subdomain works fine, e.g. `synapse.yourdomain.com` |
| Outbound internet from the VPS | Pulls Docker, the Convex backend image, and Caddy/Let's Encrypt |
| Root or `sudo` access | Installer mutates `/opt/synapse`, optionally `/etc/caddy/Caddyfile`, and Docker daemon state |

Nothing else. The installer figures out Docker, generates secrets, and
configures Caddy.

## The one-command path

SSH into the VPS, then:

```bash
git clone https://github.com/Iann29/convex-synapse.git
cd convex-synapse
./setup.sh --domain=synapse.yourdomain.com
```

Hosted curl-pipe-shell shipped in v0.6.2 — `curl -sSf
https://raw.githubusercontent.com/Iann29/convex-synapse/main/setup.sh
| bash` boots the installer straight from GitHub. v1.0.1 added the
zero-flag interactive wizard on top, so a brand-new operator can run
that exact line and walk through 4 questions instead of memorising
flags.

What `setup.sh` does, in order:

1. **Preflight** — checks OS, arch, sudo, Docker, Compose v2, disk, RAM, outbound, DNS (`dig <domain>` matches `curl ifconfig.me`)
2. **Install deps** — `apt-get install` / `dnf install` for `jq`, `curl`, `dig` (anything missing). Idempotent; skipped if all present
3. **Install dir** — creates `/opt/synapse` (or `--install-dir=`), rsyncs the repo into it
4. **Secrets** — renders `.env` from `installer/templates/env.tmpl` with `openssl rand`-generated `SYNAPSE_JWT_SECRET` + `POSTGRES_PASSWORD` (+ `SYNAPSE_STORAGE_KEY` if `--enable-ha`). On re-runs, existing values are **preserved** (Coolify-style `update_env_var`); never regenerates.
5. **Caddy** — three-mode auto-detection:
   - Caddy already on host → append managed `BEGIN/END synapse` block to `/etc/caddy/Caddyfile`, `systemctl reload caddy`
   - nginx on host → print a `location { }` snippet for the operator to paste
   - Nothing → render the standalone `Caddyfile` and activate the compose `caddy` profile (Caddy runs as a container alongside Synapse)
6. **Compose up** — `docker compose up -d --build` (builds `synapse` + `dashboard` locally; pulls `postgres:16-alpine`, `caddy:2-alpine`, etc), then waits up to 60 s for `/health` to return 200
7. **Pre-pull Convex backend image** — `docker pull ghcr.io/get-convex/convex-backend:latest` so the very first deployment doesn't hit "No such image"
8. **Self-test** — register a one-shot admin → create team → create project → create deployment → wait for `status=running` → fetch `cli_credentials` → assert URL is publicly reachable (skipped with `--no-tls` since loopback URL is expected then)
9. **Success screen** — prints public URL, install dir, log path, next steps

Total time on a 2 vCPU / 4 GB Hetzner box: **~3 min** cold, **~30 s** on
re-runs (everything's cached).

## Useful flags

| Flag | When |
|---|---|
| `--domain=<host>` | Required for TLS / `npx convex` from the outside |
| `--non-interactive` | `curl-pipe-shell` mode; no prompts; defaults everything else |
| `--no-tls` | Skip Caddy entirely. Use when fronting Synapse with your own ingress (Cloudflare Tunnel, AWS ALB, etc) or for local-only installs |
| `--skip-dns-check` | Proceed before the A-record propagates. Caddy will still fail to issue the cert until DNS lands; useful when you're only testing the install logic |
| `--enable-ha` | Opt the install into HA-per-deployment mode. Requires `SYNAPSE_BACKEND_POSTGRES_URL` + `SYNAPSE_BACKEND_S3_*` preset in env. See [HA_TESTING.md](HA_TESTING.md). |
| `--doctor` | Run preflight against an existing install. No mutations. Useful for "is my VPS still healthy?" or post-upgrade smoke-test |
| `--upgrade` | Pulls the target ref (auto-detected via GitHub `/releases/latest`, override with `--ref=<tag\|branch>`), rsyncs the new tree, runs `docker compose up -d --build`, snapshots images for rollback on health failure. v1.1.0+ admins can do the same flow with one click in the dashboard banner |
| `--uninstall` | Tears down compose, runs a mandatory pre-uninstall `--backup` first (skip with `--skip-backup`), wipes per-deployment volumes (keep with `--keep-volumes`), strips the host-Caddy managed block. Shipped in v0.6.1 |
| `--install-dir=<path>` | Override `/opt/synapse` (rare; useful for side-by-side test installs) |
| `--acme-email=<addr>` | Email for Let's Encrypt; defaults to `admin@<domain>` |

`SYNAPSE_NON_INTERACTIVE=1` env var is equivalent to `--non-interactive`.

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

Synapse's reverse proxy mounts `/d/{name}/*` on the same `:8080`
listener as the API, so a single TLS-terminating front-end protects
everything. Per-deployment ports stay internal — your firewall only
opens 80/443.

## Smoke test from your laptop

After `setup.sh` exits green, the installer's self-test already
exercised the API. To poke around manually:

```bash
# 1. Register your real admin (if the demo created one with a
#    fixture email, you'll want a "real" admin too)
curl -sf -X POST https://synapse.yourdomain.com/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@yourdomain.com","password":"strong-random","name":"You"}'

# 2. Open the dashboard
open https://synapse.yourdomain.com
```

Click **CLI credentials** on a deployment row — the export snippet
should reference `https://synapse.yourdomain.com/d/<name>`. Paste into a
local shell and `npx convex dev --once` connects from anywhere.

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
docker compose -f /opt/synapse/docker-compose.yml exec -T postgres \
  pg_dump -U synapse synapse | gzip > /backups/synapse-$(date +%F).sql.gz
```

For the Convex backend data itself: the SQLite file lives in
`synapse-data-<deployment>` per-deployment volume. Snapshot the volume
or use `npx convex export` against each deployment if you need
portable dumps.

`./setup.sh --backup` and `./setup.sh --restore=<archive>` shipped
in v0.6.1 and package the `.env`, `docker-compose.yml`, `pg_dump`,
and per-deployment Convex volumes into a single tarball. Add
`--to-s3=s3://bucket/path/` (v1.0 backup chunk) to upload after
the local bundle.

## Upgrading

**v1.1.0+ — one click from the dashboard.** When a new release
ships, a yellow banner appears at the top of the dashboard for any
team admin: "vX.Y.Z available". Click → modal with rendered release
notes → **Upgrade now** dispatches `setup.sh --upgrade` via a
host-side systemd daemon, streaming the build log to the modal. The
synapse-api restarts mid-upgrade; the modal detects it and auto-
reloads the page once health comes back.

**Pre-1.1.0, or non-systemd hosts, or scripted/CI upgrades** — manual
SSH upgrade still works the same:

```bash
cd /opt/synapse && sudo ./setup.sh --upgrade
```

`./setup.sh --upgrade` queries `/releases/latest`, snapshots image
tags for rollback, rsyncs the new tree, runs `docker compose up -d
--build`, waits for `/health` (60 s timeout), and stamps
`SYNAPSE_VERSION` on success or rolls images back on failure.
Override the target with `--ref=<tag|branch>` for testing or pinning.

Embedded migrations apply on startup — operator never runs them
manually. The provisioner queue + advisory locks keep an in-flight
deploy from being lost during the restart.

## Common gotchas

- **`npx convex` says "could not connect"** — `SYNAPSE_PUBLIC_URL` is unset or wrong. The CLI got a `http://127.0.0.1:<port>` URL that only resolves on the VPS itself. The installer sets this from `--domain`; if you ran with `--no-tls`, that's expected and `npx convex` only works from the VPS itself.
- **502 Bad Gateway from Caddy** — Synapse-API container is down. `docker compose -f /opt/synapse/docker-compose.yml logs synapse` will tell you why (usually a missing env var on first boot).
- **CORS error in browser DevTools** — `SYNAPSE_ALLOWED_ORIGINS` doesn't include the dashboard origin. `setup.sh` sets it to `https://<domain>` by default; if you front Synapse with multiple hostnames, expand the list.
- **Disk filling up** — old Convex backend images and volumes accumulate. `docker system prune` cleans dangling stuff; `docker volume ls -q --filter name=synapse-data-` shows per-deployment volumes (don't blanket-delete — these have user data).
- **Postgres volume can't be backed up while running** — `pg_dump` is the right tool, not a raw volume copy. The compose Postgres exposes `:5432` only on the docker network; use `docker compose exec` to reach it.
- **Re-running `setup.sh` doesn't refresh secrets** — by design. Existing values in `.env` are preserved (Coolify `update_env_var` pattern). To rotate the JWT secret, edit `/opt/synapse/.env` directly, then `docker compose restart synapse`.
- **Caddy fails with "no Release file for victoria"** (Linux Mint hosts) — fixed in chunk 1: the OS detection prefers `UBUNTU_CODENAME` over Mint's brand-name `VERSION_CODENAME` for Ubuntu-derived distros. If you hit this on an older repo checkout, `git pull`.

## What's NOT included today

- Multi-region replication (out of scope — Convex's per-deployment lease design forbids active-active upstream; see [`V0_5_PLAN.md`](V0_5_PLAN.md))
- A K8s / Helm install path (intentionally deferred — `setup.sh` covers the single-VPS case which is 95% of self-hosters)
- Stripe / Orb billing, WorkOS / SAML SSO, Discord / Vercel / OAuth-app integrations (cloud-only Convex features; see [`ROADMAP.md`](ROADMAP.md) "Maybe never")

For everything that *did* land — auto-installer, lifecycle commands,
hosted `curl | bash` bootstrap, first-run wizard, custom domains,
deploy keys, dashboard auto-update — see [`ROADMAP.md`](ROADMAP.md)
"Shipped".

---

## Appendix: manual install (advanced)

The `setup.sh` flow above is what the docs recommend for everyone.
This appendix is preserved for operators who need to inspect / modify
specific steps (debugging the installer itself, integrating with an
unusual ingress, reproducing what the installer would have done).

### 1. Provision the VPS

DigitalOcean / Hetzner / Linode / OVH / etc — anything with Docker
support works. After SSH'ing in:

```bash
# Debian / Ubuntu
sudo apt-get update
sudo apt-get install -y docker.io docker-compose-v2 git jq dnsutils curl

# Fedora / RHEL
sudo dnf install -y docker docker-compose-plugin git jq bind-utils curl

# (Optional) run docker without sudo
sudo usermod -aG docker $USER && newgrp docker
```

### 2. Clone Synapse + render `.env`

```bash
sudo mkdir -p /opt/synapse && sudo chown $USER /opt/synapse
git clone https://github.com/Iann29/convex-synapse.git /opt/synapse
cd /opt/synapse
cp .env.example .env
```

Edit `.env` for production:

```bash
SYNAPSE_JWT_SECRET=<output of `openssl rand -hex 64`>
SYNAPSE_PUBLIC_URL=https://synapse.yourdomain.com
SYNAPSE_PROXY_ENABLED=true
POSTGRES_USER=synapse
POSTGRES_PASSWORD=<output of `openssl rand -hex 16`>
SYNAPSE_ALLOWED_ORIGINS=https://synapse.yourdomain.com
SYNAPSE_HEALTHCHECK_VIA_NETWORK=true
```

### 3. Bring up the stack

```bash
docker compose up -d --build
docker compose logs -f synapse  # confirm "synapse starting"
```

### 4. TLS via Caddy

Install Caddy on the host (`sudo apt-get install -y caddy`), then add
to `/etc/caddy/Caddyfile`:

```caddy
synapse.yourdomain.com {
    @dashboard {
        not path /v1/* /d/* /health
    }
    handle @dashboard {
        reverse_proxy localhost:6790
    }
    handle {
        reverse_proxy localhost:8080
    }
}
```

`sudo systemctl reload caddy`. DNS A-record must already point at the
VPS — Caddy negotiates the cert on the first request.

### 5. Pre-pull the Convex backend image

```bash
docker pull ghcr.io/get-convex/convex-backend:latest
```

Otherwise the very first `create_deployment` hits "No such image" and
returns 500.

### 6. Firewall

```bash
sudo ufw allow 22 80/tcp 443/tcp
sudo ufw enable
```

Synapse (8080), dashboard (6790), Postgres (5432), and the per-
deployment 3210-3500 range stay private — Caddy is the only thing that
talks to them.

That's the manual flow. `./setup.sh` does all of the above plus the
post-install self-test. Use the manual path only when you have a
reason — the installer is faster and validates more.
