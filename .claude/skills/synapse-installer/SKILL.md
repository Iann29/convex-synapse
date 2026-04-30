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

## Don't add (anti-features from the plan)

- 20-question wizard. Default-everything-except-domain.
- Web installer that runs before the dashboard exists.
- VPS provisioning (Terraform / cloud APIs). Out of scope.
- Multi-host orchestration (K8s / Helm — v1.0+).
- Custom config language. Render `.env` from a template; that's it.
- Auto-running `synapse upgrade` from cron without explicit opt-in.
- Telemetry that sends customer data. Anonymous-only, opt-in,
  source-visible.

## When you're stuck

1. Read `docs/V0_6_INSTALLER_PLAN.md` again — most "where do I put X?"
   questions are answered there.
2. Check what Coolify did for the equivalent feature:
   https://github.com/coollabsio/coolify/blob/main/scripts/install.sh
3. Run `shellcheck` early and often — it catches half the bugs before
   they hit a test.
4. Test on a fixture first, real VPS second. The fixture catches
   "works on my Linux but not Debian" failures cheaply.

## What "done" looks like (per ticket)

A v0.6 ticket is done when:

- [ ] `bash setup.sh --doctor` passes on the relevant fixture(s)
- [ ] `shellcheck` clean
- [ ] bats tests cover the new logic
- [ ] CI's installer job is green (lint + bats)
- [ ] `docs/V0_6_INSTALLER_PLAN.md` updated if the design changed
- [ ] README's Quickstart still reflects reality after each phase
- [ ] Commit message body lists the test fixture(s) you ran against
