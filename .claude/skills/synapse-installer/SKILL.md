---
name: synapse-installer
description: Build and maintain the Synapse auto-installer (v0.6) — pure-bash setup.sh + supporting helpers + bats tests. Use when the user asks to "work on the installer", "improve setup.sh", "add a pre-flight check", "make installation easier", or anything under installer/ / setup.sh in the repo.
---

# Working on the Synapse auto-installer

The installer is v0.6's deliverable: a one-command flow that takes a
fresh VPS to a running Synapse with TLS, secrets, and a registered
admin user. Full design lives in
[`docs/V0_6_INSTALLER_PLAN.md`](../../../docs/V0_6_INSTALLER_PLAN.md).
**Read that first** before touching any installer file — it has the
phased roadmap, file layout, anti-features, and decision rationale.

## When to use this skill (vs. synapse-feature)

- This skill: anything under `setup.sh`, `installer/`, the new `caddy`
  profile in `docker-compose.yml`, or operator-facing CLI like
  `synapse status` / `upgrade` / `backup` / `doctor` / `uninstall`.
- `synapse-feature` skill: adding REST endpoints, dashboard UI, Go
  packages. Don't use that workflow for bash-only changes.

## Bash conventions

The plan picks bash deliberately (over Go) so the installer runs on
any Linux VPS without a build step. Follow these or you'll regret it
when the script fails on a fresh Debian and you can't tell why:

```bash
#!/usr/bin/env bash
set -euo pipefail

# Trap on exit to clean up partial state. Defined before any work
# starts so even an early `set -u` failure on an unset variable
# triggers it.
trap 'on_exit $?' EXIT

readonly INSTALLER_VERSION="0.6.0"
readonly LOG_FILE="/var/log/synapse-install.log"

# All sourced helpers live in installer/lib/, dot-included once at
# the top. Avoid sourcing inside conditional blocks — makes the
# control flow harder to follow.
. "$(dirname "$0")/installer/lib/detect.sh"
. "$(dirname "$0")/installer/lib/port.sh"
```

Rules:

- **`set -euo pipefail` at the top.** Every script. Catches typos,
  unset vars, broken pipes.
- **Quote every variable.** `"$foo"` not `$foo`. Even when you "know"
  it has no spaces — it'll have spaces eventually.
- **Use `[[ ]]` not `[ ]`** for tests. `[[ -n "$foo" ]]`,
  `[[ "$a" == "$b" ]]`. Standard bash, not POSIX-portable, but we're
  bash-targeted explicitly.
- **`local` everything inside functions.** `local foo="bar"`. Otherwise
  variables leak into the caller.
- **Functions return integers (0 = pass, non-zero = fail).** Use stdout
  for return values that need to be captured: `port=$(find_free_port 8080)`.
- **Echo to stderr for status, stdout for capturable output.**
  `>&2 echo "checking docker..."` for the user-visible line;
  `echo "$port"` for the value the caller will assign.
- **`shellcheck` clean.** CI runs `shellcheck -x setup.sh installer/**/*.sh`.
  No exceptions; if shellcheck is wrong, document why with a `# shellcheck disable=SC2034 reason: ...` comment.

## File layout (v0.6.0)

The plan spells this out in detail. Quick reference:

```
convex-synapse/
├── setup.sh                          # the entry point
├── installer/
│   ├── install/                      # phase scripts (one per major step)
│   │   ├── preflight.sh
│   │   ├── secrets.sh
│   │   ├── caddy.sh
│   │   ├── compose.sh
│   │   ├── verify.sh
│   │   └── ui.sh
│   ├── templates/                    # files we render or append
│   │   ├── env.tmpl
│   │   ├── caddy.fragment
│   │   └── caddy.standalone
│   ├── lib/                          # pure-function helpers
│   │   ├── detect.sh                 # has_docker, has_caddy, …
│   │   └── port.sh                   # find_free_port, port_in_use
│   └── tests/                        # bats tests
│       ├── lib_test.bats
│       ├── preflight_test.bats
│       └── fixtures/
│           ├── debian.Dockerfile
│           ├── ubuntu.Dockerfile
│           └── fedora.Dockerfile
└── docker-compose.yml                # gains optional `caddy` profile
```

## Color + UI conventions (`installer/install/ui.sh`)

The installer is a **product**, not a script. Every output goes
through the helpers below — never raw `echo` for user-facing lines.

```bash
# Color codes only via these wrappers — never hardcoded ANSI escapes
# scattered through the codebase.
ui::success() { printf '\033[32m✓\033[0m %s\n' "$*"; }
ui::warn()    { printf '\033[33m!\033[0m %s\n' "$*"; }
ui::fail()    { printf '\033[31m✗\033[0m %s\n' "$*" >&2; }
ui::info()    { printf '\033[36mℹ\033[0m %s\n' "$*"; }

# A spinner for long ops (image pulls, docker compose up).
ui::spin() {
    local msg="$1"; shift
    "$@" &
    local pid=$!
    local spin='-\|/'
    local i=0
    while kill -0 "$pid" 2>/dev/null; do
        i=$(((i + 1) % 4))
        printf '\r%s %s' "${spin:$i:1}" "$msg"
        sleep 0.1
    done
    wait "$pid" || return $?
    printf '\r✓ %s\n' "$msg"
}

# Yes/No prompt. Default = N when --non-interactive.
ui::confirm() {
    local prompt="$1" default="${2:-N}"
    [[ "${SYNAPSE_NON_INTERACTIVE:-}" == "1" ]] && {
        [[ "$default" == "Y" ]] && return 0 || return 1
    }
    local hint
    [[ "$default" == "Y" ]] && hint="[Y/n]" || hint="[y/N]"
    read -rp "$prompt $hint " ans
    ans="${ans:-$default}"
    [[ "$ans" =~ ^[Yy]$ ]]
}
```

**Don't reinvent these.** Anything that needs to print to the
operator goes through `ui::*`.

## Pre-flight check pattern

Every check in `preflight.sh` follows the same shape:

```bash
# Returns 0 (pass), 1 (warn — recoverable / offer to fix), 2 (fail — abort).
preflight::check_docker() {
    if ! command -v docker >/dev/null 2>&1; then
        ui::fail "Docker is not installed."
        ui::info "  Install with: curl -fsSL https://get.docker.com | sh"
        if ui::confirm "Install Docker now?"; then
            curl -fsSL https://get.docker.com | sh || return 2
            ui::success "Docker installed."
            return 0
        fi
        return 2
    fi

    local version
    version=$(docker version --format '{{.Server.Version}}' 2>/dev/null || echo 0)
    if [[ "$(printf '%s\n' "20.10" "$version" | sort -V | head -n1)" != "20.10" ]]; then
        ui::fail "Docker $version detected. 20.10+ required."
        ui::info "  Upgrade with: sudo apt-get install -y docker-ce"
        return 2
    fi

    ui::success "Docker $version"
    return 0
}
```

The wrapper aggregates results:

```bash
preflight::run_all() {
    local fails=0
    for check in check_os check_arch check_sudo check_docker check_compose \
                 check_disk check_ram check_outbound check_dns; do
        preflight::"$check" || (( fails += $? ))
    done
    (( fails == 0 ))
}
```

## Idempotency

The most important contract. **Re-running setup.sh on a working
install must not break it.** Specifically:

- `.env` is generated from `templates/env.tmpl` only when missing.
  Existing `.env` is parsed, validated, and re-used. Secrets are
  NEVER regenerated (would invalidate every JWT in flight).
- The Caddyfile fragment is appended only if it's not already there.
  Use `grep -q "^${MARKER_BEGIN}$" /etc/caddy/Caddyfile` with a
  marker comment so we can tell our block from the operator's.
- `docker compose up -d` is naturally idempotent; let Docker do the
  diff.
- Re-running with `--upgrade` triggers a different code path (pull
  + restart with backup); without `--upgrade`, the script does a
  health check and exits "already installed".

## Failure handling

```bash
on_exit() {
    local code=$1
    if (( code != 0 )); then
        ui::fail "Install failed at step: ${CURRENT_STEP:-unknown}"
        ui::info "Full log: $LOG_FILE"
        # Restore Caddyfile backup if we made one
        [[ -f /etc/caddy/Caddyfile.synapse-backup ]] && {
            sudo mv /etc/caddy/Caddyfile.synapse-backup /etc/caddy/Caddyfile
            ui::warn "Restored Caddyfile from backup."
        }
        # Stop any partial compose stack
        [[ -d /opt/synapse ]] && (cd /opt/synapse && docker compose down 2>/dev/null) || true
    fi
}
```

Each phase script sets `CURRENT_STEP` at the top so the trap message
is informative.

## Testing approach (bats + Docker)

Bash is hard to unit-test, but doable with bats inside disposable
Docker containers. The structure:

```
installer/tests/
├── fixtures/
│   ├── debian.Dockerfile     # FROM debian:12 — bare, no Docker
│   ├── ubuntu.Dockerfile     # FROM ubuntu:24.04 — Docker pre-installed
│   └── fedora.Dockerfile     # FROM fedora:40 — different package manager
├── lib_test.bats             # pure-function tests (port.sh, detect.sh)
├── preflight_test.bats       # exercise each check on each fixture
└── e2e_test.bats             # full setup.sh against a fixture
```

Pure-function tests run fast (no Docker):

```bash
@test "find_free_port returns the input when free" {
    run find_free_port 65000
    [ "$status" -eq 0 ]
    [ "$output" = "65000" ]
}

@test "find_free_port skips taken ports" {
    nc -l 65001 &
    PID=$!
    run find_free_port 65001
    kill "$PID"
    [ "$status" -eq 0 ]
    [ "$output" = "65002" ]
}
```

Fixture-based tests run inside the container:

```bash
@test "preflight passes on debian:12" {
    run docker run --rm -v "$BATS_TEST_DIRNAME/..:/installer" \
        synapse-installer-test:debian \
        bash /installer/setup.sh --doctor
    [ "$status" -eq 0 ]
}
```

CI runs both. Keep fixtures lean — they should boot in <5s each.

## Common gotchas

These are general bash/shell pitfalls. The next section
("Real-world bugs caught on the synapse-test VPS") has the
**v0.6.0-specific bug list** from the chunk-7 → fix-up chain — read
both before adding a new chunk.

- **`/bin/sh` is not bash on Debian.** Curl-pipe-shell installers
  often hit this. Always `#!/usr/bin/env bash` at the top of every
  file, and run via `bash setup.sh` not `sh setup.sh`.
- **`mktemp` syntax differs across systems.** Use `mktemp -d` (POSIX)
  not `mktemp -d /tmp/foo.XXXX` (BSD vs GNU differ on the suffix).
- **`grep -P` is GNU-only.** Stick to ERE (`grep -E`) so macOS dev
  loops work.
- **`readlink -f` is GNU-only.** On macOS use `realpath` or pipe
  through `python3 -c 'import os; print(os.path.realpath(...))'`.
- **`sudo` may not be installed** in container fixtures. Detect with
  `command -v sudo` and fall back to direct execution when running
  as root.
- **The Docker socket isn't mounted** in test fixtures. Tests that
  hit `docker ps` need a different fixture or stubbed binary.
- **`set -e` doesn't catch failures inside `if` / `while` / `||`**.
  This is fine — those are the bash idioms — but it means
  `if maybe_fail; then ...` will continue past a failure. Be
  intentional.
- **Color codes break in CI logs.** Wrap them in a `[[ -t 1 ]]`
  check — only emit ANSI when stdout is a TTY.

## Real-world bugs caught on the synapse-test VPS

Across v0.6.0 chunk 7, fix-ups #23/#24/#25, and the v0.6.1/v0.6.2/v0.6.3
sessions, real-VPS smokes on a fresh Hetzner CPX22 (Ubuntu 24.04)
surfaced 13 distinct bug classes that the bats suite alone could
not catch. Each one is now a regression test; read this list
before adding a chunk so the lessons don't have to be relearned:

1. **`[[ -n "$X" ]] && cmd` at the end of a function** — when the
   test is false, the function returns 1 and `set -e` aborts the
   whole script. Use explicit `if`/`fi` for any top-level
   conditional. Fixed in `setup.sh::phase_banner`,
   `secrets.sh::ensure_env`, and `caddy.sh::_render`.
2. **`docker compose pull` on services with `build:`** — synapse
   and dashboard have no published image, so pull returns "pull
   access denied" and aborts. Use `up -d --build` (which builds
   local services and pulls the rest) instead of pull-then-up.
3. **Missing `jq` and `dig` on a fresh Ubuntu** — the installer
   shells out to both. `phase_install_deps` in setup.sh apt-get-installs
   them as part of the flow; preflight checks are insufficient
   because they don't auto-install.
4. **camelCase API responses, not snake_case** — Synapse follows the
   Convex Cloud OpenAPI shape. `accessToken`, `projectId`, `convexUrl`,
   NOT `access_token`/`project_id`/`convex_url`. `verify.sh` extracts
   with both as fallback.
5. **Convex backend image needs pre-pull** — Synapse calls `docker
   run` against `ghcr.io/get-convex/convex-backend:latest` directly
   when provisioning the first deployment; without the image already
   pulled it 500s. `phase_compose_up` runs `docker pull` after
   `compose up`.
6. **`--no-tls` + `verify::check_cli_creds` is incompatible by design**
   — without a domain, `SYNAPSE_PUBLIC_URL` is empty and CLI URLs
   fall back to loopback. `verify::run --skip-cli-url-check` opts
   out. setup.sh passes the flag automatically when `NO_TLS=1` or
   `DOMAIN==""`.
7. **`SYNAPSE_PUBLIC_URL` empty on `--no-tls` blanks the dashboard**
   from a remote browser — Next.js bakes the URL at build time, so
   the JS bundle hard-coded `localhost:8080`. `setup.sh` now calls
   `detect::public_ip` (api.ipify.org → ifconfig.me) when DOMAIN is
   empty and uses `http://<ip>:<port>` as the public URL.
8. **`NEXT_PUBLIC_*` is a build-time arg, not runtime env** — even
   after passing the right `PUBLIC_SYNAPSE_URL` value to
   docker-compose, the dashboard image still had `localhost:8080`
   baked in because the Dockerfile uses `ARG NEXT_PUBLIC_*` with a
   default. docker-compose.yml now passes it as `build.args`, not
   just `environment:`.
9. **`SYNAPSE_PUBLIC_URL` lived in `.env` but never reached the
   synapse-api container** — `.env` was used for compose variable
   expansion only. The synapse service's `environment:` block didn't
   reference it. Container env was empty; `config.PublicURL` parsed
   to ""; rewrite was a no-op. Fixed in `docker-compose.yml`.
10. **`trap RETURN` fires on every nested function return**, not just
    the trap-setting function. `lifecycle::upgrade` set up a trap to
    `rm -rf $tmp_clone` — which fired the moment `ui::spin` returned
    from the snapshot phase, deleting the clone target before
    rsync ran. Fix: wrap the inner logic, cleanup once on the outer
    wrapper's return, hand the path back via `printf -v` + a
    non-shadowing var name. See `lifecycle::upgrade` /
    `lifecycle::_upgrade_inner`.
11. **`${SYNAPSE_VERSION}` as a docker tag rejects `/`.** The
    upgrade flow stamped `feat/installer-upgrade` into `.env` after
    a successful upgrade; the next compose build tagged
    `synapse-dashboard:feat/installer-upgrade` — Docker rejected with
    "invalid reference format". Two-pronged fix: pin
    `synapse:local` / `synapse-dashboard:local` in docker-compose
    (decouple tag from version) AND sanitize `/` → `-` in the
    stamp belt-and-suspenders.
12. **Compose volume names defy prediction.** The pgdata volume on
    one VPS was `synapse_synapse-pgdata` even though the install dir
    was `/opt/synapse-test` (compose project name doesn't always
    track basename — `COMPOSE_PROJECT_NAME` env, parent dir of
    compose-file, operator overrides all interact). Don't try to
    predict; iterate `docker volume ls -q | grep 'synapse-pgdata$'`
    and rm everything that matches.
13. **`pg_isready` returns 0 during postgres's first-init shutdown
    cycle.** A fresh pgdata triggers init → user-create → SHUTDOWN →
    real-restart. `pg_isready` passes during the first boot, then
    psql connections fail during the shutdown window with "the
    database system is shutting down". Wait for `psql -tAc 'SELECT 1'`
    to succeed, not pg_isready. (Also: `bash -c "pg_dump | gzip >
    out"` doesn't auto-inherit `set -o pipefail`; either set it
    inside the bash -c or decompress to a sibling .sql file and use
    a `< file` redirect — no pipe.)

Plus three follow-ups specific to v0.6.3 (first-run wizard cleanup):
- **Convex API uses `POST /<resource>/delete`, NOT HTTP `DELETE`.**
  `verify::_curl DELETE` 4xx'd silently because of `curl -f`.
- **`teams.creator_user_id ON DELETE RESTRICT` blocks any row-level
  user delete.** TRUNCATE … CASCADE is the surgical idiom for
  "reset metadata to factory state" after the self-test.
- **The `/setup` route uses `projectId` as the URL segment, not
  `projectSlug`.** The wizard tried to redirect with the slug;
  the project page fetched it as a UUID and 404'd. Match the
  existing `app/teams/[team]/page.tsx` link convention.

These are the bug classes a bats suite alone CANNOT catch. Real-VPS
validation is part of "done" for any change that touches setup.sh,
installer/, docker-compose.yml, lifecycle.sh, the auth/wizard
dashboard surface, or a backend handler that emits a URL.

## Don't add (anti-features from the plan)

- 20-question wizard. Default-everything-except-domain.
- Web installer that runs before the dashboard exists.
- VPS provisioning (Terraform / cloud APIs). Out of scope.
- Multi-host orchestration (K8s / Helm — v1.0+).
- Custom config language. Render `.env` from a template; that's it.
- Auto-running `synapse upgrade` from cron without explicit opt-in.
- Telemetry that sends customer data. Anonymous-only, opt-in,
  source-visible.

## Real-VPS smoke test workflow

The operator provisioned a Hetzner CPX22 dedicated to integration
testing. SSH alias `synapse-vps` (defined in `~/.ssh/config`,
backed by `~/.ssh/synapse-test-vps`). Credentials in `/.vps/`
(gitignored). Reset is free — operator clicks one button on the
Hetzner Cloud Console.

For any change that touches `setup.sh`, `installer/`, `docker-compose.yml`,
or a backend handler that emits a URL:

```bash
# 1. Push your branch
git push -u origin <branch>

# 2. Tear down the previous test install (preserves nothing)
ssh synapse-vps 'docker compose -f /opt/synapse-test/docker-compose.yml down -v 2>/dev/null
                 docker rm -f $(docker ps -aq --filter label=synapse.managed=true) 2>/dev/null
                 rm -rf /tmp/convex-synapse /opt/synapse-test'

# 3. Clone the branch and run setup.sh end-to-end
ssh synapse-vps 'cd /tmp && git clone -b <branch> https://github.com/Iann29/convex-synapse.git
                 cd convex-synapse && bash setup.sh --no-tls --skip-dns-check --non-interactive --install-dir=/opt/synapse-test'

# 4. Validate from outside (your dev machine, NOT the VPS)
curl -sf http://178.105.62.81:8080/health   # synapse healthy?
curl -sf -o /dev/null -w "%{http_code}\n" http://178.105.62.81:6790/register   # dashboard renders?
```

If something needs a clean OS image (kernel state, package cache),
ask the operator to reset via the Hetzner console — they offered
free resets for exactly this reason.

## When you're stuck

1. Read `docs/V0_6_INSTALLER_PLAN.md` again — most "where do I put X?"
   questions are answered there.
2. Check what Coolify did for the equivalent feature:
   https://github.com/coollabsio/coolify/blob/main/scripts/install.sh
3. Run `shellcheck` early and often — it catches half the bugs before
   they hit a test.
4. Test on a fixture first, real VPS second. The fixture catches
   "works on my Linux but not Debian" failures cheaply.
5. If a remote browser sees something different from what curl shows,
   it's a build-time vs runtime config gap (see bug #8 above).

## What "done" looks like (per ticket)

A v0.6 ticket is done when:

- [ ] `bash setup.sh --doctor` passes on the relevant fixture(s)
- [ ] `shellcheck` clean
- [ ] bats tests cover the new logic
- [ ] CI's installer job is green (lint + bats)
- [ ] **Real-VPS smoke** passes for any setup.sh / compose / handler-URL change (`ssh synapse-vps` workflow above)
- [ ] `docs/V0_6_INSTALLER_PLAN.md` updated if the design changed
- [ ] README's Quickstart still reflects reality after each phase
- [ ] Commit message body lists the test fixture(s) you ran against
