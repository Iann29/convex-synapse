#!/usr/bin/env bash
#
# Synapse — auto-installer entry point.
#
# Run on a fresh VPS via the hosted one-liner:
#     curl -sSf https://raw.githubusercontent.com/Iann29/convex-synapse/main/setup.sh \
#         | bash -s -- --domain=synapse.example.com
#
# Or, after `git clone`:
#     ./setup.sh --domain=synapse.example.com
#
# The script is the v0.6 north-star: 90% of single-VPS installs work
# end-to-end with no manual file edits. Pre-flight checks, secret
# generation, reverse-proxy detection, compose bring-up, post-install
# self-test — all driven from one phased flow.
#
# Everything lives inside `main()` and is invoked at EOF. This is the
# Tailscale curl-pipe-shell idiom: a download truncated mid-file
# can't execute partial logic because the function is never called.
#
# When invoked via `curl | bash`, the installer/ directory holding
# the dot-included libraries doesn't ship alongside this single file.
# `setup::needs_bootstrap` detects that case and `setup::bootstrap`
# git-clones the repo into a temp dir + re-execs setup.sh from there
# with the same args. Operators who ran `git clone` see no behaviour
# change — `installer/` is already next to setup.sh.

set -Eeuo pipefail

readonly INSTALLER_VERSION="1.2.0"
readonly INSTALL_DIR_DEFAULT="/opt/synapse"
readonly LOG_FILE="${SYNAPSE_INSTALL_LOG:-/tmp/synapse-install.log}"
readonly LOCK_FILE="/var/lock/synapse-installer.lock"
readonly BOOTSTRAP_REPO_URL="${SYNAPSE_BOOTSTRAP_REPO_URL:-https://github.com/Iann29/convex-synapse.git}"
readonly BOOTSTRAP_REF="${SYNAPSE_BOOTSTRAP_REF:-main}"

# Resolve the directory holding this script so we can dot-include the
# installer libraries regardless of how the operator invoked us
# (./setup.sh, /opt/synapse/setup.sh, bash -c "$(curl …)", etc).
# `${BASH_SOURCE[0]}` is the path of the file currently executing —
# but under `curl | bash`, bash reads from stdin and BASH_SOURCE[0]
# is unset/empty. Default to "" first (set -u guard), then resolve
# to an absolute path only when the source file actually exists.
# An empty HERE is the bootstrap signal — we'll re-exec from a
# real clone before sourcing any libs.
_setup_src="${BASH_SOURCE[0]:-}"
if [[ -n "$_setup_src" && -f "$_setup_src" ]]; then
    HERE="$(cd "$(dirname "$_setup_src")" && pwd)"
else
    HERE=""
fi
unset _setup_src
readonly INSTALLER_TEMPLATES="${HERE:+$HERE/installer/templates}"
export INSTALLER_TEMPLATES

# CURRENT_STEP is set at the top of each phase so the trap message
# tells the operator which step crashed without a stack trace.
CURRENT_STEP="initialising"

# Trap on EXIT (any exit, success or failure) for cleanup; trap on
# ERR (any errexit-triggering failure) for the failure-context dump.
# `set -E` makes ERR propagate into shell functions and command
# substitutions — without it the trap fires only on the top-level
# script.
trap 'on_err $? "${BASH_LINENO[0]}" "${FUNCNAME[1]:-main}" "${BASH_COMMAND}"' ERR
trap 'on_exit $?' EXIT

on_err() {
    local rc=$1 line=$2 fn=$3 cmd=$4
    {
        printf '\n[FATAL] %s failed (exit %d) at line %d in %s()\n' \
            "$CURRENT_STEP" "$rc" "$line" "$fn"
        printf '  command: %s\n' "$cmd"
        printf '  log: %s\n' "$LOG_FILE"
    } >&2
}

on_exit() {
    local rc=$1
    if (( rc != 0 )); then
        # Best-effort rollback. None of these are fatal — the err
        # message above is what the operator actually reads.
        if [[ -f "${SYNAPSE_CADDYFILE_BACKUP:-}" ]]; then
            mv -f "${SYNAPSE_CADDYFILE_BACKUP}" "${SYNAPSE_CADDYFILE_PATH:-/etc/caddy/Caddyfile}" 2>/dev/null \
                && ui::warn "Restored Caddyfile from backup."
        fi
    fi
    rm -f "$LOCK_FILE.fd" 2>/dev/null || true
}

# usage / help -------------------------------------------------------

usage() {
    cat <<EOF
Synapse installer ${INSTALLER_VERSION}

Usage:
    setup.sh [options]

Options:
    --domain=<host>          Public hostname for Synapse (e.g.
                             synapse.example.com). Required for
                             non-interactive installs.
    --base-domain=<host>     Wildcard subdomain for per-deployment
                             URLs (e.g. synapse.example.com → each
                             deployment becomes <name>.synapse.example.com).
                             Requires *.<host> DNS pointed at this VPS;
                             Caddy on-demand TLS issues certs lazily.
                             v1.0+. Empty = use the legacy
                             <PublicURL>/d/<name>/ proxy form.
    --acme-email=<address>   Email for Let's Encrypt account. Defaults
                             to admin@<domain>.
    --enable-ha              Enable HA mode (requires
                             SYNAPSE_BACKEND_POSTGRES_URL +
                             SYNAPSE_BACKEND_S3_* preset in env).
    --no-tls                 Skip Caddy / TLS configuration. Use when
                             a separate ingress fronts Synapse.
    --skip-dns-check         Skip the A-record / public-IP check in
                             preflight (useful when DNS hasn't
                             propagated yet).
    --non-interactive        Disable all prompts. Implies sane defaults.
    --upgrade                Upgrade an existing install: clone the
                             target ref, rebuild, restart, on failure
                             roll back to the previous images.
    --ref=<git-ref>          Target branch or tag for --upgrade.
                             Default: latest GitHub release (or 'main'
                             if no releases exist yet).
    --force                  Skip the "already on latest" short-circuit
                             during --upgrade.
    --backup                 Snapshot the install (.env + compose +
                             pg_dump + per-deployment volumes) into a
                             timestamped tarball.
    --out=<path>             Output path for --backup. Default:
                             \$INSTALL_DIR/backups/synapse-backup-<ts>.tar.gz
    --exclude-env            Skip .env in --backup (no secrets in tarball).
    --to-s3=<s3-uri>         With --backup, also upload to s3://bucket/key
                             after the local bundle. Trailing slash =
                             treat as directory (auto-suffix archive name).
                             Requires AWS_ACCESS_KEY_ID +
                             AWS_SECRET_ACCESS_KEY in env. For S3-
                             compatible (Backblaze, R2, Wasabi, MinIO):
                             also set SYNAPSE_BACKUP_S3_ENDPOINT.
    --restore=<archive>      Wipe + restore from a backup tarball. Local
                             path or s3://bucket/key URI (auto-downloaded).
    --keep-env               During --restore, keep the current .env
                             instead of replacing it from the archive.
    --doctor                 Run preflight + status checks against an
                             existing install; no mutations.
    --uninstall              Remove the install + containers. Takes a
                             pre-uninstall backup unless --skip-backup;
                             wipes volumes unless --keep-volumes (a
                             volume without its matching .env can't
                             be reused — recover via re-install +
                             --restore=<backup>).
    --skip-backup            Skip the pre-uninstall backup (use only
                             when you're sure you don't need rollback).
    --keep-volumes           Preserve synapse-data-* + pgdata. Only
                             useful if you saved .env outside the
                             install dir.
    --logs=<component>       Stream docker compose logs for a single
                             service (synapse, dashboard, postgres,
                             caddy, convex-dashboard,
                             convex-dashboard-proxy).
    --follow                 With --logs, keep tailing (docker compose
                             logs --follow). Default: one-shot tail.
    --tail=<n>               With --logs, lines of history to print
                             before --follow (default: 200).
    --status                 Print a read-only diagnostic snapshot of
                             the install: containers, volumes, public
                             URL, DNS, TLS expiry, disk. Exit 0 healthy,
                             1 degraded, 2 broken.
    --install-dir=<path>     Override $INSTALL_DIR_DEFAULT.
    --no-bootstrap           Skip the curl|sh bootstrap re-exec even
                             when installer/ is missing. Useful for
                             tests and for running setup.sh from a
                             custom checkout.
    --version                Print installer version and exit.
    --help                   This message.

Environment:
    SYNAPSE_NON_INTERACTIVE  Same as --non-interactive (any non-empty)
    SYNAPSE_INSTALL_LOG      Override log path (default $LOG_FILE)
    SYNAPSE_BOOTSTRAP_REPO_URL  Git URL to clone for the bootstrap
                             re-exec (default $BOOTSTRAP_REPO_URL)
    SYNAPSE_BOOTSTRAP_REF    Git ref to check out during bootstrap
                             (default $BOOTSTRAP_REF)
EOF
}

# CLI parsing --------------------------------------------------------

parse_flags() {
    DOMAIN=""
    BASE_DOMAIN=""
    ACME_EMAIL=""
    ENABLE_HA=0
    NO_TLS=0
    SKIP_DNS=0
    DOCTOR=0
    UPGRADE=0
    UPGRADE_REF=""
    FORCE=0
    BACKUP=0
    BACKUP_OUT=""
    BACKUP_TO_S3=""
    EXCLUDE_ENV=0
    RESTORE_ARCHIVE=""
    KEEP_ENV=0
    UNINSTALL=0
    SKIP_BACKUP=0
    KEEP_VOLUMES=0
    LOGS_COMPONENT=""
    LOGS_FOLLOW=0
    LOGS_TAIL=""
    STATUS=0
    NO_BOOTSTRAP=0
    INSTALL_DIR="$INSTALL_DIR_DEFAULT"
    # INSTALL_DIR_FROM_FLAG = 1 means the operator passed --install-dir=
    # explicitly. The wizard reads this to know whether to ask the
    # install-dir question (no flag = ask; flag = respect operator's
    # choice). Without this we'd skip Step 3 silently because the
    # default value is always non-empty.
    INSTALL_DIR_FROM_FLAG=0
    while (( $# > 0 )); do
        case "$1" in
            --domain=*)        DOMAIN="${1#*=}" ;;
            --base-domain=*)   BASE_DOMAIN="${1#*=}" ;;
            --acme-email=*)    ACME_EMAIL="${1#*=}" ;;
            --enable-ha)       ENABLE_HA=1 ;;
            --no-tls)          NO_TLS=1 ;;
            --skip-dns-check)  SKIP_DNS=1 ;;
            --non-interactive) export SYNAPSE_NON_INTERACTIVE=1 ;;
            --upgrade)         UPGRADE=1 ;;
            --ref=*)           UPGRADE_REF="${1#*=}" ;;
            --force)           FORCE=1 ;;
            --backup)          BACKUP=1 ;;
            --out=*)           BACKUP_OUT="${1#*=}" ;;
            --to-s3=*)         BACKUP_TO_S3="${1#*=}" ;;
            --exclude-env)     EXCLUDE_ENV=1 ;;
            --restore=*)       RESTORE_ARCHIVE="${1#*=}" ;;
            --keep-env)        KEEP_ENV=1 ;;
            --doctor)          DOCTOR=1 ;;
            --uninstall)       UNINSTALL=1 ;;
            --skip-backup)     SKIP_BACKUP=1 ;;
            --keep-volumes)    KEEP_VOLUMES=1 ;;
            --logs=*)          LOGS_COMPONENT="${1#*=}" ;;
            --logs)            LOGS_COMPONENT="${2:-}"; shift ;;
            --follow)          LOGS_FOLLOW=1 ;;
            --tail=*)          LOGS_TAIL="${1#*=}" ;;
            --tail)            LOGS_TAIL="${2:-}"; shift ;;
            --status)          STATUS=1 ;;
            --install-dir=*)   INSTALL_DIR="${1#*=}"; INSTALL_DIR_FROM_FLAG=1 ;;
            --no-bootstrap)    NO_BOOTSTRAP=1 ;;
            --version)         echo "synapse-installer $INSTALLER_VERSION"; exit 0 ;;
            --help|-h)         usage; exit 0 ;;
            *) echo "unknown flag: $1" >&2; usage >&2; exit 2 ;;
        esac
        shift
    done
    if [[ -z "$ACME_EMAIL" && -n "$DOMAIN" ]]; then
        ACME_EMAIL="admin@$DOMAIN"
    fi
}

# Single-instance lock (flock on FD 9 — survives subshells, releases
# on process exit). Without this, two operators running setup.sh on
# the same VPS could pick the same auto-allocated port and one would
# fail with "bind: address already in use".
acquire_lock() {
    exec 9>"$LOCK_FILE.fd" || {
        echo "could not open $LOCK_FILE.fd" >&2
        exit 2
    }
    if ! flock -n 9; then
        echo "another setup.sh is already running (lock: $LOCK_FILE.fd)" >&2
        exit 2
    fi
}

# Bootstrap (curl | bash) ---------------------------------------------
#
# When the script is fetched via `curl | bash`, only setup.sh exists
# in the bash process — the installer/ tree of dot-includable libs
# is missing. setup::needs_bootstrap detects that state; setup::bootstrap
# clones the repo into a temp dir and re-execs setup.sh from the clone,
# passing through every original argument.
#
# Both functions are pure and side-effect-free except for the obvious
# (clone, exec, stdout chatter), which keeps them testable in isolation
# under __SETUP_NO_MAIN.

# setup::needs_bootstrap [<dir>]
# Returns 0 (true) when no installer/ tree is reachable from <dir>
# (default: $HERE). Empty <dir> always counts as needing bootstrap —
# that's the curl|bash case, where BASH_SOURCE[0] resolved to nothing.
setup::needs_bootstrap() {
    local dir="${1-$HERE}"
    if [[ -z "$dir" ]]; then
        return 0
    fi
    if [[ ! -d "$dir/installer" ]]; then
        return 0
    fi
    return 1
}

# setup::bootstrap_target_dir
# Echoes the path we'll clone into. /tmp first; falls back to
# $HOME/.synapse-bootstrap-<pid> when /tmp isn't writable (some
# hardened distros mount /tmp read-only or noexec). $$ is the pid of
# the running bash, so two parallel curl|bash invocations don't
# clobber each other.
setup::bootstrap_target_dir() {
    local pid=$$
    if [[ -d /tmp && -w /tmp ]]; then
        printf '/tmp/convex-synapse-bootstrap-%s' "$pid"
        return 0
    fi
    local home="${HOME:-/root}"
    if [[ -d "$home" && -w "$home" ]]; then
        printf '%s/.synapse-bootstrap-%s' "$home" "$pid"
        return 0
    fi
    return 1
}

# setup::bootstrap "$@"
# Clone the repo into a temp dir and exec setup.sh from there with the
# original args. This function MUST run before source_libs() — that's
# the whole point. On success it execs (never returns); on failure it
# prints a manual recovery hint and exits non-zero.
setup::bootstrap() {
    if ! command -v git >/dev/null 2>&1; then
        printf 'error: git is required for the curl|bash bootstrap.\n' >&2
        printf '  install it first: sudo apt-get install -y git  (Debian/Ubuntu)\n' >&2
        printf '                    sudo dnf install -y git      (Fedora/RHEL)\n' >&2
        printf '  then re-run the one-liner.\n' >&2
        printf '  alternatively, git clone %s and run ./setup.sh directly.\n' \
            "$BOOTSTRAP_REPO_URL" >&2
        exit 2
    fi
    local target
    if ! target="$(setup::bootstrap_target_dir)"; then
        # shellcheck disable=SC2016  # literal $HOME in operator-facing message
        printf 'error: no writable temp dir for bootstrap (/tmp + $HOME both unusable).\n' >&2
        printf '  manually: git clone %s /opt/synapse-src && /opt/synapse-src/setup.sh ...\n' \
            "$BOOTSTRAP_REPO_URL" >&2
        exit 2
    fi
    # Clean up a stale dir from a previous aborted bootstrap with the
    # same pid (rare — pid reuse — but cheap to handle).
    if [[ -e "$target" ]]; then
        rm -rf "$target"
    fi
    printf 'Bootstrapping Synapse installer from %s (ref: %s)...\n' \
        "$BOOTSTRAP_REPO_URL" "$BOOTSTRAP_REF" >&2
    printf '  target: %s\n' "$target" >&2
    if ! git clone --depth=1 --branch "$BOOTSTRAP_REF" \
            "$BOOTSTRAP_REPO_URL" "$target" >&2; then
        printf 'error: git clone failed. Check network + that the ref %s exists.\n' \
            "$BOOTSTRAP_REF" >&2
        exit 2
    fi
    if [[ ! -x "$target/setup.sh" ]]; then
        printf 'error: cloned repo has no setup.sh at %s/setup.sh\n' "$target" >&2
        exit 2
    fi
    printf 'Re-executing setup.sh from %s\n' "$target" >&2
    # Tell the child it's the bootstrapped clone so it doesn't loop.
    # The exec replaces this process; cleanup of $target is left to
    # the OS / next reboot. Operators that want it gone immediately
    # can rm -rf /tmp/convex-synapse-bootstrap-* themselves.
    export SYNAPSE_BOOTSTRAPPED=1
    exec "$target/setup.sh" "$@"
}

# Source library files. We source ALL of them up-front so undefined
# functions surface as runtime errors at the right moment, not deep
# inside a phase.
source_libs() {
    # shellcheck source=installer/lib/detect.sh
    . "$HERE/installer/lib/detect.sh"
    # shellcheck source=installer/lib/port.sh
    . "$HERE/installer/lib/port.sh"
    # shellcheck source=installer/install/ui.sh
    . "$HERE/installer/install/ui.sh"
    # shellcheck source=installer/install/wizard.sh
    . "$HERE/installer/install/wizard.sh"
    # shellcheck source=installer/install/preflight.sh
    . "$HERE/installer/install/preflight.sh"
    # shellcheck source=installer/install/secrets.sh
    . "$HERE/installer/install/secrets.sh"
    # shellcheck source=installer/install/caddy.sh
    . "$HERE/installer/install/caddy.sh"
    # shellcheck source=installer/install/compose.sh
    . "$HERE/installer/install/compose.sh"
    # shellcheck source=installer/install/verify.sh
    . "$HERE/installer/install/verify.sh"
    # shellcheck source=installer/install/s3.sh
    . "$HERE/installer/install/s3.sh"
    # shellcheck source=installer/install/lifecycle.sh
    . "$HERE/installer/install/lifecycle.sh"
    # shellcheck source=installer/install/updater.sh
    . "$HERE/installer/install/updater.sh"
}

# phase_wizard — interactive Q&A when `curl | bash` runs without
# mode-defining flags. Sets DOMAIN / NO_TLS / BASE_DOMAIN / ENABLE_HA /
# INSTALL_DIR + SYNAPSE_AUTO_INSTALL_DOCKER from the operator's
# answers. Bails the install with exit 130 if they decline at the
# summary confirm. No-ops in --non-interactive mode and when the
# operator already answered everything via flags.
phase_wizard() {
    CURRENT_STEP="wizard"
    if ! wizard::should_run; then
        return 0
    fi
    wizard::run
}

# phase_autoinstall_docker — when the wizard agreed to fix missing
# dependencies, install Docker via the official one-liner BEFORE
# the preflight runs. preflight then sees a working Docker and
# moves on instead of failing the whole script. Idempotent: the
# get.docker.com script no-ops when docker is already present.
phase_autoinstall_docker() {
    CURRENT_STEP="autoinstall_docker"
    # Interactive path: operator already said yes via the wizard.
    # Non-interactive root path: we treat "auto-install when missing"
    # as the sensible default, since the alternative is "fail with a
    # one-line cure right next to the abort message", which is the
    # frustration the wizard was built to avoid.
    if (( ${SYNAPSE_AUTO_INSTALL_DOCKER:-0} == 0 )) \
            && (( ${NON_INTERACTIVE:-0} == 0 )); then
        return 0
    fi
    if detect::has_docker; then
        return 0
    fi
    if [[ "$EUID" -ne 0 ]] && ! detect::has_cmd sudo; then
        ui::fail "Docker missing and we don't have root/sudo to install it"
        return 2
    fi
    ui::step "Installing Docker (one-time, ~1 min)"
    if ! wizard::install_docker; then
        ui::fail "Docker auto-install failed — see $LOG_FILE"
        return 2
    fi
}

# Phases -------------------------------------------------------------

phase_banner() {
    CURRENT_STEP="banner"
    ui::step "Synapse installer v${INSTALLER_VERSION}"
    ui::info "Install dir: $INSTALL_DIR"
    ui::info "Log file: $LOG_FILE"
    # Under `set -e`, a top-level `[[ -n "$X" ]] && cmd` propagates
    # the test's exit code when the test is false, aborting the
    # whole script. Use `if`/`fi` for any bare top-level conditional;
    # the short-circuit form is only safe inside another `if` /
    # `||` / `while` / function-with-trailing-true.
    if [[ -n "$DOMAIN" ]]; then
        ui::info "Domain: $DOMAIN"
    fi
    if [[ -n "$BASE_DOMAIN" ]]; then
        ui::info "Custom domains: *.${BASE_DOMAIN#.} (per-deployment subdomains)"
    fi
}

phase_preflight() {
    CURRENT_STEP="preflight"
    ui::step "Pre-flight checks"
    local domain_arg="$DOMAIN"
    if (( SKIP_DNS )); then
        domain_arg=""
    fi
    # Pass BASE_DOMAIN through the env so preflight::run_all can pick
    # up check_base_domain (--skip-dns-check disables it just like
    # --domain's check_dns).
    local base_domain_for_preflight="$BASE_DOMAIN"
    if (( SKIP_DNS )); then
        base_domain_for_preflight=""
    fi
    if ! SYNAPSE_BASE_DOMAIN="$base_domain_for_preflight" \
            preflight::run_all "$domain_arg"; then
        return 2
    fi
}

# phase_install_deps — verify.sh shells out to `jq` (response parsing)
# and `curl` (HTTP); preflight check_dns shells out to `dig`. Most
# fresh distros ship curl, but jq and dig (dnsutils / bind-utils)
# need an explicit install. Doing it as its own phase makes the
# operator-visible step order clear: preflight ✓, deps ✓, install …
phase_install_deps() {
    CURRENT_STEP="install_deps"
    local missing=()
    detect::has_cmd jq   || missing+=(jq)
    detect::has_cmd curl || missing+=(curl)
    detect::has_cmd dig  || missing+=(dig)
    if (( ${#missing[@]} == 0 )); then
        return 0
    fi
    ui::step "Installing missing tools: ${missing[*]}"
    local prefix
    prefix="$(detect::sudo_cmd 2>/dev/null || true)"
    local pkg
    pkg="$(detect::pkg_manager)"
    case "$pkg" in
        apt)
            # `dig` lives in the dnsutils package on Debian/Ubuntu.
            local apt_pkgs=()
            local m
            for m in "${missing[@]}"; do
                case "$m" in
                    dig) apt_pkgs+=(dnsutils) ;;
                    *)   apt_pkgs+=("$m") ;;
                esac
            done
            $prefix apt-get update -qq
            DEBIAN_FRONTEND=noninteractive $prefix apt-get install -y -qq "${apt_pkgs[@]}"
            ;;
        dnf|yum)
            local rpm_pkgs=()
            local m
            for m in "${missing[@]}"; do
                case "$m" in
                    dig) rpm_pkgs+=(bind-utils) ;;
                    *)   rpm_pkgs+=("$m") ;;
                esac
            done
            $prefix "$pkg" install -y "${rpm_pkgs[@]}"
            ;;
        pacman)
            local arch_pkgs=()
            local m
            for m in "${missing[@]}"; do
                case "$m" in
                    dig) arch_pkgs+=(bind-tools) ;;
                    *)   arch_pkgs+=("$m") ;;
                esac
            done
            $prefix pacman -S --noconfirm "${arch_pkgs[@]}"
            ;;
        apk)
            local apk_pkgs=()
            local m
            for m in "${missing[@]}"; do
                case "$m" in
                    dig) apk_pkgs+=(bind-tools) ;;
                    *)   apk_pkgs+=("$m") ;;
                esac
            done
            $prefix apk add --no-cache "${apk_pkgs[@]}"
            ;;
        *)
            ui::fail "Don't know how to install ${missing[*]} on this distro (pkg=$pkg)"
            ui::info "  Install manually and re-run setup.sh"
            return 2
            ;;
    esac
    ui::success "Tools installed: ${missing[*]}"
}

phase_install_dir() {
    CURRENT_STEP="install_dir"
    ui::step "Preparing $INSTALL_DIR"
    local prefix=""
    prefix="$(detect::sudo_cmd 2>/dev/null || true)"
    if [[ ! -d "$INSTALL_DIR" ]]; then
        $prefix mkdir -p "$INSTALL_DIR"
    fi
    # Copy the repo files into INSTALL_DIR if we're running from
    # somewhere else (e.g. operator unpacked the tarball into /tmp
    # and is upgrading /opt/synapse). Idempotent rsync-style copy.
    if [[ "$HERE" != "$INSTALL_DIR" ]]; then
        if detect::has_cmd rsync; then
            $prefix rsync -a --delete-excluded \
                --exclude='.git' --exclude='node_modules' \
                "$HERE/" "$INSTALL_DIR/"
        else
            # cp -a is good enough; rsync just runs faster.
            $prefix cp -a "$HERE/." "$INSTALL_DIR/"
        fi
    fi
    ui::success "$INSTALL_DIR is ready"
}

phase_secrets() {
    CURRENT_STEP="secrets"
    ui::step "Generating secrets + rendering .env"
    local env_file="$INSTALL_DIR/.env"
    local prefix=""
    prefix="$(detect::sudo_cmd 2>/dev/null || true)"
    # Render the template only if .env doesn't exist yet. After that,
    # ensure_env handles fill-in-missing on subsequent runs.
    if [[ ! -f "$env_file" ]]; then
        # Pre-populate operator-supplied placeholder values BEFORE
        # rendering so the template gets the right values in one pass.
        export POSTGRES_USER="synapse"
        export POSTGRES_DB="synapse"
        export POSTGRES_PORT="5432"
        export SYNAPSE_PORT="${SYNAPSE_PORT:-8080}"
        export DASHBOARD_PORT="${DASHBOARD_PORT:-6790}"
        export CONVEX_DASHBOARD_PORT="${CONVEX_DASHBOARD_PORT:-6791}"
        # Decide the public URL the operator's *browser* and *CLI* will
        # use to reach this Synapse:
        #   --domain set       -> https://<domain> (Caddy fronts it)
        #   --no-tls + IP detected -> http://<public-ip>:<port> (no TLS,
        #                              but reachable from outside the VPS)
        #   --no-tls + IP undetectable -> "" (legacy localhost; install
        #                              is local-only, dashboard won't
        #                              load from a remote browser)
        # The same value lands in BOTH SYNAPSE_PUBLIC_URL (server-side,
        # used by /cli_credentials) and PUBLIC_SYNAPSE_URL (which
        # docker-compose passes to the dashboard as
        # NEXT_PUBLIC_SYNAPSE_URL — the URL the dashboard JS calls).
        # Without this wiring, a remote browser hitting the dashboard
        # at http://<vps-ip>:6790 silently fails because the JS tries
        # to reach the API at localhost:8080 — the operator's own
        # machine, where nothing is listening.
        #
        # Same pattern for PUBLIC_CONVEX_DASHBOARD_URL — the URL the
        # "Open dashboard" button targets. Points at the convex-dashboard
        # service we run alongside Synapse on port 6791. Different host
        # (or sub-path) options are reserved for v0.6.3 + custom Caddy
        # routes; for now the simple "<host>:6791" form covers 100% of
        # operators who don't already run the standalone elsewhere.
        local public_url="" public_dash_url="" allowed_origins="*"
        if [[ -n "$DOMAIN" ]]; then
            public_url="https://$DOMAIN"
            public_dash_url="https://$DOMAIN:${CONVEX_DASHBOARD_PORT}"
            allowed_origins="https://$DOMAIN"
        else
            local detected_ip=""
            if detected_ip="$(detect::public_ip 2>/dev/null)" && [[ -n "$detected_ip" ]]; then
                public_url="http://${detected_ip}:${SYNAPSE_PORT}"
                public_dash_url="http://${detected_ip}:${CONVEX_DASHBOARD_PORT}"
                ui::info "Detected public IP $detected_ip — wiring Synapse to $public_url"
                ui::info "Convex Dashboard wired to $public_dash_url"
            else
                ui::warn "Public IP not detected — dashboard will only work from this VPS"
                ui::info "  (set --domain=<host> for an externally-reachable install)"
            fi
        fi
        export SYNAPSE_PUBLIC_URL="$public_url"
        export SYNAPSE_ALLOWED_ORIGINS="$allowed_origins"
        export PUBLIC_CONVEX_DASHBOARD_URL="$public_dash_url"
        # Custom domains (v1.0+). Operator passes --base-domain or
        # presets SYNAPSE_BASE_DOMAIN. Strip leading dots so operators
        # who type ".synapse.example.com" by accident don't end up
        # with double dots in URLs.
        export SYNAPSE_BASE_DOMAIN="${BASE_DOMAIN#.}"
        local ha_flag="false"
        (( ENABLE_HA )) && ha_flag="true"
        export SYNAPSE_HA_ENABLED="$ha_flag"
        export SYNAPSE_VERSION="${SYNAPSE_VERSION:-${INSTALLER_VERSION}}"
        # Fill secrets first so they appear in the rendered template,
        # not as empty placeholders. Two-line assign-then-export so
        # the generator's exit code isn't masked by `export` itself
        # always returning 0 (SC2155).
        local jwt pg sk=""
        jwt="$(secrets::gen_jwt)"
        pg="$(secrets::gen_db_password)"
        export SYNAPSE_JWT_SECRET="$jwt"
        export POSTGRES_PASSWORD="$pg"
        if (( ENABLE_HA )); then
            sk="$(secrets::gen_storage_key)"
        fi
        export SYNAPSE_STORAGE_KEY="$sk"
        secrets::render_env_tmpl "$INSTALLER_TEMPLATES/env.tmpl" "$env_file"
        $prefix chmod 0600 "$env_file"
        ui::success ".env rendered (${#SYNAPSE_JWT_SECRET}-byte JWT secret, ${#POSTGRES_PASSWORD}-byte PG password)"
    else
        # Existing install — preserve secrets, top-up missing ones.
        local args=()
        (( ENABLE_HA )) && args+=(--ha)
        secrets::ensure_env "$env_file" "${args[@]}"
        ui::success ".env exists; preserved existing secrets"
    fi
}

phase_caddy() {
    CURRENT_STEP="caddy"
    if (( NO_TLS )); then
        ui::step "Skipping reverse-proxy configuration (--no-tls)"
        CADDY_PROFILE_NEEDED=0
        return 0
    fi
    if [[ -z "$DOMAIN" ]]; then
        ui::warn "No --domain set — skipping Caddy configuration"
        CADDY_PROFILE_NEEDED=0
        return 0
    fi
    ui::step "Configuring reverse proxy"
    export DOMAIN
    export SYNAPSE_PORT="${SYNAPSE_PORT:-8080}"
    export DASHBOARD_PORT="${DASHBOARD_PORT:-6790}"
    export ACME_EMAIL
    local mode
    mode="$(caddy::detect_mode)"
    case "$mode" in
        caddy_host)
            ui::info "Caddy detected on host — appending managed block"
            local caddy_file="${CADDY_FILE:-/etc/caddy/Caddyfile}"
            if [[ -f "$caddy_file" ]]; then
                local backup="$caddy_file.synapse-backup-$$"
                local prefix=""
                prefix="$(detect::sudo_cmd 2>/dev/null || true)"
                $prefix cp -a "$caddy_file" "$backup" || true
                export SYNAPSE_CADDYFILE_BACKUP="$backup"
                export SYNAPSE_CADDYFILE_PATH="$caddy_file"
            fi
            caddy::install_host_block "$caddy_file"
            CADDY_PROFILE_NEEDED=0
            ui::success "Caddy reloaded"
            ;;
        nginx_external)
            ui::warn "nginx detected — Synapse can't auto-edit your config"
            ui::info "Paste this snippet into your server { } block:"
            caddy::print_nginx_snippet
            ui::info "Then: sudo nginx -t && sudo systemctl reload nginx"
            CADDY_PROFILE_NEEDED=0
            ;;
        caddy_compose)
            ui::info "No reverse proxy detected — bringing up Caddy in compose"
            local out="$INSTALL_DIR/Caddyfile"
            CADDY_FORCE_OVERWRITE=1 caddy::write_standalone "$out"
            export SYNAPSE_CADDYFILE_PATH="$out"
            CADDY_PROFILE_NEEDED=1
            ui::success "Caddyfile written to $out"
            ;;
        *)
            ui::fail "caddy::detect_mode returned unknown mode: $mode"
            return 2
            ;;
    esac
}

phase_compose_up() {
    CURRENT_STEP="compose_up"
    ui::step "Bringing up the Synapse stack"
    local profile_args=()
    if (( ${CADDY_PROFILE_NEEDED:-0} )); then
        profile_args+=(--profile caddy)
    fi
    # We deliberately skip `compose::pull` and rely on `up -d --build`
    # instead. Reason: the `synapse` and `dashboard` services in our
    # docker-compose.yml have `build:` directives and no published
    # image, so `docker compose pull` returns "pull access denied"
    # and aborts. `up --build` builds them locally and pulls only
    # the services that actually have a registry image (postgres,
    # caddy, etc). The longer build time on first install is the
    # cost; subsequent runs reuse the layer cache.
    compose::up "$INSTALL_DIR" "${profile_args[@]}" --build
    local synapse_url="http://localhost:${SYNAPSE_PORT:-8080}"
    if ! compose::wait_healthy "$synapse_url/health" 60; then
        ui::fail "Synapse didn't become healthy in 60s — check 'docker compose logs synapse'"
        return 2
    fi
    ui::success "Synapse is healthy"

    # Pre-pull the Convex backend image. Synapse calls `docker run`
    # against this image when provisioning the very first deployment;
    # without it pre-pulled, the first create_deployment hits
    # "No such image: ghcr.io/get-convex/convex-backend:latest" and
    # returns 500. Pulling here turns first-deployment latency into
    # known-and-visible install time (the image is ~150 MB).
    local backend_image="${SYNAPSE_BACKEND_IMAGE:-ghcr.io/get-convex/convex-backend:latest}"
    ui::spin "Pre-pulling $backend_image (first run, ~150MB)" \
        docker pull "$backend_image"
}

phase_verify() {
    CURRENT_STEP="verify"
    ui::step "Self-test: register → team → project → deployment"
    local synapse_url="http://localhost:${SYNAPSE_PORT:-8080}"
    # Default: full teardown after success. Leaves the install in a
    # zero-user / zero-team state so the v0.6.3 first-run wizard at
    # /setup fires when the operator opens the dashboard. Operators
    # who want a pre-baked demo deployment can re-run setup.sh with
    # SYNAPSE_VERIFY_KEEP=1 (keeps the demo + the self-test admin —
    # the wizard then short-circuits to /login).
    local verify_args=()
    if [[ -n "${SYNAPSE_VERIFY_KEEP:-}" ]]; then
        verify_args+=(--keep-demo)
    fi
    # When the operator ran with --no-tls or didn't supply --domain,
    # SYNAPSE_PUBLIC_URL is empty and /cli_credentials returns the
    # legacy loopback URL. That's the documented dev/local-only
    # mode — not a misconfiguration. Skip the public-reachability
    # assertion in that case so the self-test still finishes green.
    if (( NO_TLS )) || [[ -z "$DOMAIN" ]]; then
        verify_args+=(--skip-cli-url-check)
    fi
    if ! verify::run "$synapse_url" "${verify_args[@]}"; then
        ui::fail "Self-test failed — Synapse is up but the API isn't behaving"
        return 2
    fi
}

phase_success_screen() {
    CURRENT_STEP="success"
    # Resolve the URL the operator should actually open in a browser:
    #   - HTTPS via Caddy when --domain is set
    #   - HTTP on the dashboard port (6790) otherwise — NOT the API
    #     port (8080); the dashboard is what humans interact with.
    # When neither domain nor public IP is detected, fall back to a
    # localhost URL with a hint (dev-mode only).
    local public_url
    if [[ -n "${DOMAIN:-}" ]]; then
        public_url="https://${DOMAIN}"
    elif [[ -n "${SYNAPSE_DETECTED_PUBLIC_IP:-}" ]]; then
        public_url="http://${SYNAPSE_DETECTED_PUBLIC_IP}:6790"
    else
        public_url="http://localhost:6790"
    fi

    # Build a coloured success banner that visually mirrors the wizard
    # banner. Colours render iff stdout is a TTY (or being teed through
    # one — UI_FORCE_COLOR=1 covers that case if the operator wants).
    # Going straight to stdout (no /dev/tty redirect) keeps it simple:
    # the install log captures the same content with ANSI escapes
    # baked in, which is what brew / k3s / devcontainer-cli all do
    # — `less -R` and `cat` render it fine, and operators almost
    # never grep the log with bare eyes.
    local C_GREEN="" C_BOLD="" C_DIM="" C_CYAN="" C_RESET=""
    if [[ -z "${UI_NO_COLOR:-}" ]] && [[ -z "${NO_COLOR:-}" ]] \
            && { [[ -t 1 ]] || [[ "${UI_FORCE_COLOR:-0}" == "1" ]] || { : >/dev/tty; } 2>/dev/null; }; then
        C_GREEN=$'\033[32m'
        C_BOLD=$'\033[1m'
        C_DIM=$'\033[2m'
        C_CYAN=$'\033[36m'
        C_RESET=$'\033[0m'
    fi

    cat <<EOF

  ${C_GREEN}╭─────────────────────────────────────────────────────────╮${C_RESET}
  ${C_GREEN}│${C_RESET}                                                         ${C_GREEN}│${C_RESET}
  ${C_GREEN}│${C_RESET}    ${C_GREEN}✓${C_RESET}  ${C_BOLD}Synapse is ready${C_RESET}                                  ${C_GREEN}│${C_RESET}
  ${C_GREEN}│${C_RESET}                                                         ${C_GREEN}│${C_RESET}
  ${C_GREEN}╰─────────────────────────────────────────────────────────╯${C_RESET}

  ${C_BOLD}Open in your browser:${C_RESET}
      ${C_CYAN}${public_url}${C_RESET}

  ${C_DIM}Install dir:${C_RESET}  ${INSTALL_DIR}
  ${C_DIM}Log:${C_RESET}          ${LOG_FILE}

  ${C_BOLD}Next steps${C_RESET}
    ${C_GREEN}1.${C_RESET} Open the URL above and register your admin user
    ${C_GREEN}2.${C_RESET} Create a team → project → deployment
    ${C_GREEN}3.${C_RESET} Use the CLI snippet from the deployment row to run \`npx convex deploy\`

  ${C_BOLD}Useful commands${C_RESET}
    ${C_DIM}${INSTALL_DIR}/setup.sh --doctor${C_RESET}    re-run health checks
    ${C_DIM}${INSTALL_DIR}/setup.sh --upgrade${C_RESET}   pull the latest release + restart
    ${C_DIM}${INSTALL_DIR}/setup.sh --status${C_RESET}    diagnostic snapshot
    ${C_DIM}${INSTALL_DIR}/setup.sh --backup${C_RESET}    snapshot the install (use --to-s3=... for S3)
    ${C_DIM}docker compose -f ${INSTALL_DIR}/docker-compose.yml logs -f synapse${C_RESET}

EOF

    # When custom domains are enabled, the operator MUST have wildcard
    # DNS pointed at this VPS for Caddy on-demand TLS to issue certs.
    # The DNS preflight already warned them if the wildcard isn't
    # resolving, but a reminder at the end of the install (when the
    # success state is fresh) is the cheapest insurance against
    # "I created a deployment, the URL doesn't load" support tickets.
    if [[ -n "${BASE_DOMAIN:-}" ]]; then
        local stripped="${BASE_DOMAIN#.}"
        cat <<EOF
  ${C_BOLD}Custom domains enabled (v1.0)${C_RESET}
    Each provisioned deployment becomes a dedicated subdomain:
        ${C_CYAN}https://<deployment-name>.${stripped}${C_RESET}

    ${C_DIM}⚠ DNS: *.${stripped} A record must point at this VPS${C_RESET}
    ${C_DIM}  (Caddy can't issue Let's Encrypt certs until it resolves)${C_RESET}
    ${C_DIM}  Probe: dig +short probe-test.${stripped}${C_RESET}

EOF
    fi
}

# main ----------------------------------------------------------------

main() {
    parse_flags "$@"

    # Bootstrap re-exec: when the script was fetched via curl|bash
    # the installer/ libs aren't on disk next to us. Clone the repo
    # into a temp dir and exec from there. --no-bootstrap escapes
    # the loop for tests; SYNAPSE_BOOTSTRAPPED=1 is set by the
    # parent setup.sh on the re-exec so we don't clone twice if the
    # cloned copy somehow still looks bootstrap-y.
    if (( ! NO_BOOTSTRAP )) && [[ -z "${SYNAPSE_BOOTSTRAPPED:-}" ]] \
            && setup::needs_bootstrap; then
        setup::bootstrap "$@"
        # setup::bootstrap execs; if we reach this line it failed.
        exit 2
    fi

    # Doctor mode — run preflight against the existing install and
    # exit. No mutations.
    if (( DOCTOR )); then
        source_libs
        ui::step "Synapse installer ${INSTALLER_VERSION} — doctor mode"
        preflight::run_all "$DOMAIN"
        exit $?
    fi

    if (( UPGRADE )); then
        # Mirror tee-to-log only AFTER we know we're upgrading — a
        # bare --version / --help call shouldn't create a dangling log.
        if [[ -w "$(dirname "$LOG_FILE")" ]]; then
            exec > >(tee -a "$LOG_FILE") 2>&1
        fi
        source_libs
        local up_args=()
        if [[ -n "$UPGRADE_REF" ]]; then
            up_args+=(--ref="$UPGRADE_REF")
        fi
        if (( FORCE )); then
            up_args+=(--force)
        fi
        lifecycle::upgrade "$INSTALL_DIR" "${up_args[@]}"
        exit $?
    fi
    if (( BACKUP )); then
        if [[ -w "$(dirname "$LOG_FILE")" ]]; then
            exec > >(tee -a "$LOG_FILE") 2>&1
        fi
        source_libs
        local bk_args=()
        if [[ -n "$BACKUP_OUT" ]]; then
            bk_args+=(--out="$BACKUP_OUT")
        fi
        if (( EXCLUDE_ENV )); then
            bk_args+=(--exclude-env)
        fi
        if [[ -n "$BACKUP_TO_S3" ]]; then
            bk_args+=(--to-s3="$BACKUP_TO_S3")
        fi
        lifecycle::backup "$INSTALL_DIR" "${bk_args[@]}"
        exit $?
    fi
    if [[ -n "$RESTORE_ARCHIVE" ]]; then
        if [[ -w "$(dirname "$LOG_FILE")" ]]; then
            exec > >(tee -a "$LOG_FILE") 2>&1
        fi
        source_libs
        local rs_args=()
        if (( KEEP_ENV )); then
            rs_args+=(--keep-env)
        fi
        if [[ -n "${SYNAPSE_NON_INTERACTIVE:-}" ]]; then
            rs_args+=(--non-interactive)
        fi
        lifecycle::restore "$INSTALL_DIR" "$RESTORE_ARCHIVE" "${rs_args[@]}"
        exit $?
    fi
    if (( UNINSTALL )); then
        if [[ -w "$(dirname "$LOG_FILE")" ]]; then
            exec > >(tee -a "$LOG_FILE") 2>&1
        fi
        source_libs
        local un_args=()
        if (( SKIP_BACKUP )); then
            un_args+=(--skip-backup)
        fi
        if (( KEEP_VOLUMES )); then
            un_args+=(--keep-volumes)
        fi
        if [[ -n "${SYNAPSE_NON_INTERACTIVE:-}" ]]; then
            un_args+=(--non-interactive)
        fi
        lifecycle::uninstall "$INSTALL_DIR" "${un_args[@]}"
        exit $?
    fi
    if [[ -n "$LOGS_COMPONENT" ]]; then
        # No tee-to-log: the operator wants the raw stream (often piped
        # to less / grep). Mixing in installer log noise would break
        # those pipelines.
        source_libs
        local lg_args=()
        if (( LOGS_FOLLOW )); then
            lg_args+=(--follow)
        fi
        if [[ -n "$LOGS_TAIL" ]]; then
            lg_args+=(--tail="$LOGS_TAIL")
        fi
        lifecycle::logs "$INSTALL_DIR" "$LOGS_COMPONENT" "${lg_args[@]}"
        exit $?
    fi
    if (( STATUS )); then
        source_libs
        lifecycle::status "$INSTALL_DIR"
        exit $?
    fi

    # Capture all output (stdout + stderr) to the log from this point
    # forward. Operator still sees the live stream because tee writes
    # to stdout AND the file. We do this AFTER parsing so a `--help`
    # invocation doesn't create a dangling log.
    if [[ -w "$(dirname "$LOG_FILE")" ]]; then
        exec > >(tee -a "$LOG_FILE") 2>&1
    fi

    source_libs
    acquire_lock
    phase_banner
    phase_wizard
    phase_autoinstall_docker
    phase_preflight
    phase_install_deps
    phase_install_dir
    phase_secrets
    phase_install_updater
    phase_caddy
    phase_compose_up
    phase_verify
    phase_success_screen
}

# Skip the actual run when sourced from a test (so bats can probe
# individual functions without triggering preflight + compose). The
# Tailscale truncation-safety property is preserved: a half-downloaded
# script still won't reach this line.
if [[ -z "${__SETUP_NO_MAIN:-}" ]]; then
    main "$@"
fi
