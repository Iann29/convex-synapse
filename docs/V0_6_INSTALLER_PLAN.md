# v0.6 — Auto-installer plan

> **Status:** Scoping document. Not yet started — this is the next major
> milestone after v0.5. Authored after a friction audit on the existing
> "clone, edit .env, edit Caddyfile, sudo reload, docker compose up"
> install path: too many manual steps for a tool whose explicit goal is
> to make managing Convex deployments easy.

## North star

```
$ curl -sf https://get.synapse.dev | sh
```

Two minutes later, the operator's VPS has Synapse running on
`https://<their-domain>` with TLS, a registered admin user, and the
Convex backend image pre-pulled. They open the URL, log in, click
**New deployment**, and 1 second later have a running Convex backend.

Synapse's reason to exist is to make self-hosting Convex
painless. If installing Synapse itself is painful, we've failed at the
same problem we're trying to solve. Treat the installer as a
**first-class product surface**, not a build-and-deploy afterthought.

## Why this matters

The Convex Cloud experience is "click click click — you have a working
deployment." The Synapse alternative today is:

1. SSH into VPS
2. Install Docker (if missing)
3. `git clone …`
4. `cp .env.example .env` and edit ~5 values
5. Generate `SYNAPSE_JWT_SECRET` with `openssl rand`
6. Generate `SYNAPSE_STORAGE_KEY` (if HA)
7. Add a Caddy block for the subdomain
8. `sudo systemctl reload caddy`
9. Open firewall (`ufw allow 80/443`)
10. `docker compose up -d`
11. Wait for the image pull
12. Smoke-test from a remote machine
13. Realise `npx convex` doesn't work because `SYNAPSE_PUBLIC_URL`
    wasn't set, edit `.env` again, restart

That's a 30-minute thing the **first** time, and it's full of
opportunities to get a step wrong. We need a one-command flow.

## Inspirations to study before coding

These are the gold-standard installers in the same conceptual space.
The agent picking up this plan should read at least 2-3 of them before
writing a single line:

- **Coolify** — `curl -fsSL https://cdn.coollabs.io/coolify/install.sh | bash`. Self-hosted PaaS. Detects OS, installs Docker, generates all secrets, prints credentials at the end. Most direct comparable to what we want.
- **k3s** — `curl -sfL https://get.k3s.io | sh -`. Detects systemd, installs binary, sets up the service. Very lean.
- **Tailscale** — `curl -fsSL https://tailscale.com/install.sh | sh`. Same one-liner, plus a follow-up interactive auth flow. Good model for "one-command install + browser-based finish".
- **Caddy** — apt repo + `caddy run`. Minimal config, sane defaults. Their "config-as-code" story is what we want to mimic for the Caddyfile fragment we generate.
- **Plausible** — `wget -O- https://plausible.io/install.sh | bash`. Simple shell installer for a Docker-Compose-based product, very close to our shape.
- **Pocketbase** — single binary, runs anywhere. Different model (no Docker dependency), but shows what "minimal friction" looks like.

The most directly applicable lesson: **the installer is a CLI experience first**, not a config file. Users should never edit YAML
or `.env` by hand for the happy path.

## Phased roadmap

### v0.6.0 — Foundation (this milestone)

`./setup.sh` script + supporting compose changes. Targets: 90% of
single-VPS installs work end-to-end without manual file edits.

- Pre-flight checks (Docker, ports, DNS, disk, RAM)
- Interactive wizard with sane defaults (or `--non-interactive --domain=...`)
- Auto-generated secrets
- Caddy detection (use existing OR install OR run-in-compose)
- Idempotent (re-run = re-validate, don't break existing install)
- Self-test after install
- Print a pretty success screen with the URL + admin credentials

### v0.6.1 — Lifecycle commands

`./synapse <verb>` for ongoing maintenance. Same script, but exposes
subcommands once installed.

- `synapse status` — diagnostic snapshot (containers, ports, DNS, TLS, disk)
- `synapse upgrade` — `git pull && docker compose pull && up -d` with rollback
- `synapse backup` — `pg_dump` + per-deployment volume snapshots → tarball
- `synapse restore <backup.tar>` — reverse of the above
- `synapse uninstall` — remove everything, with mandatory backup-first prompt
- `synapse logs <component>` — aggregated logs (synapse / dashboard / postgres / caddy)
- `synapse doctor` — runs the pre-flight checks against an existing install

### v0.6.2 — Remote-friendly install

Hosted install script + curl one-liner. Requires:

- Static script hosted at `get.synapse.dev` (or GitHub Pages stub if no domain)
- Versioned via git tag — install script pins against a tag, not `main`
- `--version=v0.6.0` flag to install a specific release

### v0.6.3 — Browser-driven first-run wizard

After `setup.sh` finishes (or after `docker compose up -d` from a clone),
the dashboard's `/login` redirects to `/setup` if no users exist yet.
The wizard walks through:

1. Create admin user (sets first email/password)
2. Configure cluster name (optional cosmetic)
3. Optional: enable HA mode (collects Postgres URL, S3 creds, validates them)
4. Smoke-test: provision a "demo" deployment, verify it works, offer to keep or delete
5. Show the CLI snippet so the operator can immediately `npx convex deploy`

This is what makes the experience feel **finished** — operator never
sees a config file.

### v0.6.4 — Cloud images (stretch)

Pre-built DigitalOcean / Hetzner / Linode snapshots with Synapse
pre-installed. One-click deploy from each provider's dashboard. 60
seconds to a running Synapse.

- Packer template that builds the snapshot
- GitHub Action that runs Packer on each tagged release
- Listings on each provider's marketplace (Coolify-style)

Out of scope for the initial v0.6 milestone — bookmark for v0.7.

## v0.6.0 — Detailed design

The MVP. Deliverable: a working `./setup.sh` that the README points
to. After this, operators can stand up a VPS in 2 minutes.

### File layout

```
convex-synapse/
├── setup.sh                          # the one entry point
├── installer/
│   ├── install/
│   │   ├── preflight.sh              # OS / Docker / port / DNS / disk / RAM checks
│   │   ├── secrets.sh                # JWT + storage key generation
│   │   ├── caddy.sh                  # detect + configure Caddy
│   │   ├── compose.sh                # render .env, run docker compose
│   │   ├── verify.sh                 # post-install health checks
│   │   └── ui.sh                     # ANSI colors + prompts + spinner
│   ├── templates/
│   │   ├── env.tmpl                  # .env with {{PLACEHOLDERS}}
│   │   ├── caddy.fragment            # Caddyfile block to append
│   │   └── caddy.standalone          # Full Caddyfile when no Caddy exists
│   └── lib/
│       ├── detect.sh                 # has_docker, has_caddy, has_systemd, etc.
│       └── port.sh                   # find_free_port, port_in_use
├── docker-compose.yml                # gain optional `caddy` profile (v0.6 brings it)
└── .env.example                      # stays around for the manual path
```

### `setup.sh` flow

```
┌─────────────────────────────────────────────────────────────┐
│ 1. Banner + version check                                    │
│    Pulls "current latest tag" from GitHub API; warns if      │
│    user is on an older script.                               │
└─────────────────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────────────────┐
│ 2. Pre-flight (preflight.sh)                                 │
│    ✓ OS: Linux distro detected (Debian/Ubuntu/Fedora/etc)    │
│    ✓ Architecture: x86_64 / aarch64                          │
│    ✓ Privileges: sudo or root available                      │
│    ✓ Docker: version >= 20.10 (or "would you like to         │
│       install it?" with apt/dnf/brew commands)               │
│    ✓ Compose v2: `docker compose version`                    │
│    ✓ Disk: ≥ 10GB free on /                                  │
│    ✓ RAM: ≥ 2GB                                              │
│    ✓ Outbound: can reach ghcr.io (image pull will work)      │
│                                                              │
│    Each line color-coded ✓ green / ✗ red / ! yellow.         │
│    Hard fails (Docker missing without auto-install) print    │
│    a remediation command and exit non-zero.                  │
└─────────────────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────────────────┐
│ 3. Choices (interactive, or read from flags)                 │
│    → Domain: prompt, then `dig +short` it; warn if A record  │
│       doesn't match `curl ifconfig.me`. Allow override with  │
│       --skip-dns-check.                                      │
│    → Reverse proxy: detect existing Caddy/nginx              │
│         a) Caddy on host → "Add a block to your Caddyfile?"  │
│         b) Nginx on host → "Print a config snippet for you   │
│            to add manually"                                  │
│         c) None → "Run Caddy in compose? (recommended)"      │
│    → HA mode: "Enable HA per-deployment? (requires           │
│       external Postgres + S3 — defer to docs/HA_TESTING.md)" │
│    → Auto-update: "Run `synapse upgrade` weekly via cron?"   │
│    → Telemetry: "Anonymous usage metrics? (opt-in)"          │
└─────────────────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────────────────┐
│ 4. Port allocation (port.sh)                                 │
│    For each target port (8080, 5432, 6790, 3210-3500),       │
│    `ss -tulpnH` to check in-use; auto-pick a free            │
│    alternative and write to env. Print the chosen ports      │
│    so the operator knows what to expose.                     │
└─────────────────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────────────────┐
│ 5. Generate secrets (secrets.sh)                             │
│    SYNAPSE_JWT_SECRET=$(openssl rand -hex 64)                │
│    SYNAPSE_STORAGE_KEY=$(openssl rand -hex 32) (HA only)     │
│    POSTGRES_PASSWORD=$(openssl rand -hex 16)                 │
│    Stored in /opt/synapse/.env (chmod 600).                  │
└─────────────────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────────────────┐
│ 6. Render configuration (compose.sh)                         │
│    .env from env.tmpl with all collected values              │
│    docker-compose.yml stays vendored (no rendering needed)   │
│    Caddy block appended (or full Caddyfile written if        │
│    Synapse is bringing its own Caddy)                        │
└─────────────────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────────────────┐
│ 7. Bring up the stack                                        │
│    `docker compose pull` (warns if pull fails — image not    │
│       reachable, network problem)                            │
│    `docker compose up -d`                                    │
│    Wait for /health to return 200 (60s timeout, with         │
│       a spinner).                                            │
└─────────────────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────────────────┐
│ 8. Self-test (verify.sh)                                     │
│    POST /v1/auth/register → first admin user                 │
│    POST /v1/teams/create_team → "Default" team               │
│    POST /v1/teams/default/create_project → "Demo" project    │
│    POST /v1/projects/<id>/create_deployment → spin up dev    │
│    Wait for status=running                                   │
│    GET /v1/deployments/<name>/cli_credentials                │
│    Confirm it returns the public URL (not 127.0.0.1).        │
│    Tear down the demo deployment OR keep it (operator        │
│      choice in step 3).                                      │
└─────────────────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────────────────┐
│ 9. Success screen                                            │
│    ✓ Synapse is ready                                        │
│    URL: https://synapse.example.com                          │
│    Admin email: ian@example.com                              │
│    Admin password: <one-time> (printed once, never stored)   │
│                                                              │
│    Next steps:                                               │
│      1. Open the URL and log in                              │
│      2. Click "New deployment" to provision your first       │
│         Convex backend                                       │
│      3. Use `npx convex deploy` from anywhere with the       │
│         CLI credentials from the dashboard                   │
│                                                              │
│    Useful commands:                                          │
│      ./synapse status   — health check                       │
│      ./synapse upgrade  — pull latest version                │
│      ./synapse backup   — back up the metadata DB            │
└─────────────────────────────────────────────────────────────┘
```

### Pre-flight check spec

Each function in `preflight.sh` returns 0 (pass), 1 (warn — recoverable),
2 (fail — must abort). The wrapper aggregates and only proceeds if no
fails.

```bash
check_os()              # Linux only; refuses macOS/Windows for now
check_arch()            # x86_64 / aarch64; warns on others
check_sudo()            # Either running as root OR sudo configured
check_docker()          # >= 20.10; offers to install on Debian/Ubuntu/Fedora
check_compose()         # `docker compose version` returns >= 2.0
check_disk_free()       # ≥ 10 GB on the filesystem holding /var/lib/docker
check_ram()             # ≥ 2 GB total
check_outbound()        # `curl -sf https://ghcr.io` succeeds
check_dns(domain)       # `dig +short $domain` matches `curl ifconfig.me`
check_port_free(port)   # `ss -tulpn` doesn't show the port
check_caddy()           # which caddy / systemctl is-active caddy
check_nginx()           # which nginx / systemctl is-active nginx
check_existing_install() # /opt/synapse exists? upgrade path triggers
```

When `check_docker()` returns 1 ("not installed"), the script offers:

```
Docker is not installed. Install it now? (y/N)
  > y
[runs the right install command for the detected OS]
[verifies install succeeded]
[continues]
```

When it returns 2 ("installed but version too old"), the script aborts
with a clear remediation:

```
✗ Docker 19.03 detected. Synapse requires 20.10 or newer.
  Upgrade with:
    sudo apt-get update && sudo apt-get install -y docker-ce
  Then re-run this installer.
```

### Caddy detection logic

```
if has_caddy_systemd:
    # Existing Caddy on host. Append a block to /etc/caddy/Caddyfile.
    # Idempotent: parse the file, only add if not already present.
    # On reload failure, restore the backup we made at the start.
    mode="caddy_host"

elif has_caddy_container:
    # Caddy running in some other compose stack. Print instructions
    # for the operator to add a block themselves; we can't reach into
    # someone else's compose.
    mode="caddy_external_compose"
    print_snippet
    exit_with_manual_instructions

elif has_nginx:
    # Nginx user. Print an nginx config block; can't auto-install.
    mode="nginx_external"
    print_snippet
    exit_with_manual_instructions

else:
    # Fresh VPS, no reverse proxy. Run Caddy as part of our own
    # compose stack via the new `caddy` profile.
    mode="caddy_compose"
    enable_caddy_profile
```

The `caddy` profile in `docker-compose.yml` adds a Caddy service that
mounts a generated `Caddyfile` (via volume) and forwards `:80/:443` to
the synapse-network. It's behind a profile so existing operators with
their own Caddy don't get a port conflict.

### Idempotency contract

Re-running `setup.sh` on an existing install must:

1. Detect `/opt/synapse` exists
2. Validate the existing `.env` (still has all required values?)
3. Compare the script version to the installed version
4. If newer: offer `synapse upgrade` flow
5. If same: re-run pre-flight + verify, treat as a `synapse doctor`

Never blindly overwrite `.env`, never regenerate secrets that already
exist (would invalidate every JWT and admin key in flight).

### Failure handling

Every step gets a `cleanup` trap that runs on non-zero exit:

- Restore the Caddyfile backup (taken before append)
- Stop the partially-up compose stack
- Leave `.env` in place (so re-runs are faster) but mark it
  `# INSTALL FAILED — do not edit, re-run setup.sh`

Print a single-line failure message + which step failed + a
`/var/log/synapse-install.log` link with the full trace.

### CLI surface

```bash
./setup.sh                                  # interactive, defaults
./setup.sh --domain=synapse.example.com     # non-interactive
./setup.sh --domain=... --no-tls            # skip Caddy (DIY)
./setup.sh --domain=... --enable-ha          # collect HA cluster info
./setup.sh --upgrade                         # rerun for upgrade flow
./setup.sh --doctor                          # diagnostics, no changes
./setup.sh --uninstall                       # explicit removal flow
./setup.sh --version                         # print installer version
```

After install, a symlink `synapse → /opt/synapse/setup.sh` lives in
`/usr/local/bin`, so the operator can run `synapse status` etc. from
anywhere.

## v0.6.0 — Concrete tickets

Land in this order. Each is its own PR, each must be green in CI
before the next one starts.

| # | Title | Estimate |
|---|---|---|
| 1 | `installer/lib/detect.sh` + `port.sh` — pure-bash helpers, fully unit-tested with bats | 0.5 day |
| 2 | `installer/install/preflight.sh` + colored `ui.sh` — every check above | 1 day |
| 3 | `installer/install/secrets.sh` + `installer/templates/env.tmpl` — secret generation + .env render, idempotent | 0.5 day |
| 4 | `installer/install/caddy.sh` + `installer/templates/caddy.*` — three-mode Caddy detection + append | 1 day |
| 5 | `docker-compose.yml` `caddy` profile + `installer/templates/caddy.standalone` | 0.5 day |
| 6 | `installer/install/compose.sh` + `verify.sh` — render + bring up + self-test | 1 day |
| 7 | `setup.sh` — the entry point that orchestrates phases 1-9 | 1 day |
| 8 | bats integration tests (Docker container with a fresh OS image, run setup.sh against it) | 1.5 days |
| 9 | README rewrite: replace the "Quickstart" section with the one-liner | 0.5 day |
| 10 | `docs/PRODUCTION.md` rewrite: now reads "run setup.sh"; manual flow demoted to an appendix | 0.5 day |

**Total estimate: ~8 days** of focused work. With part-time pace
(3-4 h/day) → 3-4 calendar weeks for the full v0.6.0.

## Key design decisions

### Pure bash, not Go

The installer must run on a fresh VPS that doesn't have Go, doesn't
have Node, doesn't have anything except a base Linux image. Bash + the
standard POSIX toolset (curl, jq, openssl, ss, dig) is the only
universal denominator. Tradeoff: bash testing is harder, but `bats` +
container-based fixtures gets us most of the way.

The day Synapse needs richer logic than bash can comfortably express,
the right answer is a tiny Go binary that's pre-compiled and
statically linked, downloaded by a tiny bootstrap shell wrapper —
roughly the k3s pattern. Don't preemptively bootstrap that.

### `/opt/synapse/` as the install location

Not `~/synapse/` — the installer assumes a multi-user system where the
operator may not be the one running upgrades. Convention matches what
the Production guide already recommends.

The compose stack runs as the operator's user (no separate `synapse`
system user — Docker compose handles process isolation), but the
files live in a system path so `synapse upgrade` works the same way
regardless of who's logged in.

### No silent network configuration

The installer never touches `iptables` directly. It opens UFW rules
because UFW is the de facto Debian/Ubuntu firewall, but if `ufw` isn't
installed the script just prints "open ports 80 and 443 in your
firewall manually" and continues. Surprising the operator with
firewall changes is a recipe for losing SSH access.

### Telemetry is opt-IN, anonymous, transparent

A check-the-box-during-install moment. If they opt in, the installer
sends:

- Anonymous installation ID (hash of `hostname`)
- OS / arch / Docker version
- Synapse version
- Mode (proxy/host-port, HA on/off)
- Number of deployments (rough bucket: 1-5, 5-20, 20+)

`POST` to a stats endpoint we control. Source-visible in the
installer, easy to disable post-install (`synapse telemetry off`).
**Never send a single bit of customer data** — no email, no team
names, no deployment names. Pin this in the contributor guidelines.

### Print credentials, don't store them in plain

The first admin password is printed exactly once, never written to
disk. If the operator misses it, they re-run the password-reset flow
(which we'd need to add — bookmark for v0.6.1 if missing today).

## Open questions

- Should the `setup.sh` host be `get.synapse.dev` (we register a
  domain) or just the GitHub raw URL? Domain reads better
  (`curl get.synapse.dev | sh`) but adds a registration + renewal
  responsibility. Default decision: use raw GitHub URL via a stable
  redirect to the latest tag, defer the domain to v0.6.2.
- Multi-tenant clusters: should one VPS be able to host multiple
  Synapse instances (e.g. one per customer)? **No** for v0.6 —
  install path assumes the VPS is dedicated to Synapse. If an
  operator wants multi-tenancy they spin up multiple VPSes (or
  contribute the multi-instance support themselves later).
- HA-mode setup: does `setup.sh` know how to provision MinIO + a
  backing Postgres for the operator? **No** — too much surface
  area, too many decisions (which managed Postgres? cost?).
  The installer collects pre-existing `BACKEND_*` URLs; the
  HA_TESTING.md compose profile remains the dev path.
- Auto-update: `cron`-driven `synapse upgrade` is a safety risk
  (silent breakage of an in-flight deployment). Default: prompt
  on install, **off by default**, with a clear "you can enable
  later with `synapse autoupdate on`".

## Anti-features (do NOT add)

- A wizard with 20 questions. Pick reasonable defaults for everything;
  `--non-interactive` must work with just `--domain`.
- A web-based installer (browser flow before the dashboard exists).
  Browser-driven first-run is a v0.6.3 dashboard feature, not an
  installer feature — separation of concerns.
- Provisioning the VPS itself (Terraform / Pulumi / cloud APIs).
  The cloud-image story (v0.6.4) covers that segment differently.
- Multi-host orchestration. K8s / Helm is v1.0+, scoped separately.
- A custom config language. The current env-var contract is fine;
  the installer just generates `.env` with sane values.

## Success metrics

We'll know v0.6.0 is good when:

- A new contributor can stand up Synapse on a fresh Hetzner VPS,
  start to finish, in **< 5 minutes** including domain DNS
  propagation
- The README's "Quickstart" section fits in 3 lines (curl, wait,
  open URL)
- The discord/issue tracker stops getting "how do I install this"
  questions
- A friend who hasn't seen Synapse can install + provision their
  first Convex backend without asking the maintainer anything

## What this DOESN'T solve

Calling out the v0.6 boundary so the agent picking this up doesn't
sprawl scope:

- **Custom domains per deployment** — `myapp.example.com` instead of
  `synapse.example.com/d/myapp`. Caddy supports this trivially but
  needs a DNS+cert dance per deployment. v1.0.
- **Volume backups → S3** automated. Manual `pg_dump` script is
  good enough for v0.6.1; cloud-backed scheduled backups are v1.0.
- **Telemetry dashboard** to actually look at the opt-in data we
  collect. v0.7.
- **Web-based password reset** for the first admin. Should land in
  v0.6.1 alongside the lifecycle commands — without it, losing the
  install-time password means re-running the install.
- **Multi-region installer** awareness. Out of scope; documented in
  V0_5_PLAN.md.

## Pointer to inspirations (file paths)

When the agent picks this up, these are the most useful starting
points to read in order:

1. https://github.com/coollabsio/coolify/blob/main/scripts/install.sh
   — the closest functional analog to what we want
2. https://github.com/k3s-io/k3s/blob/master/install.sh — gold
   standard for "how lean can a Linux installer be"
3. https://github.com/tailscale/tailscale/blob/main/cmd/installer/install.sh
   — production-grade installer pattern with auto-updater
4. https://caddyserver.com/docs/install — Caddy's own install story,
   specifically the apt-repo bit we'd reuse
5. The `docs/PRODUCTION.md` in this repo — the manual flow this
   plan is replacing

## Ownership

This file is the source of truth for what v0.6 looks like. When the
agent that implements it learns something the plan got wrong, they
should update this doc in the same PR that lands the code change.
That keeps the plan honest and gives the next person reading it the
ground truth, not the optimistic-day-one version.
