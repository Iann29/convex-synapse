#!/usr/bin/env bash
#
# Synapse — auto-installer entry point.
#
# Run on a fresh VPS:
#     curl -sf https://get.synapse.dev | sh
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

set -Eeuo pipefail

readonly INSTALLER_VERSION="0.6.0"
readonly INSTALL_DIR_DEFAULT="/opt/synapse"
readonly LOG_FILE="${SYNAPSE_INSTALL_LOG:-/tmp/synapse-install.log}"
readonly LOCK_FILE="/var/lock/synapse-installer.lock"

# Resolve the directory holding this script so we can dot-include the
# installer libraries regardless of how the operator invoked us
# (./setup.sh, /opt/synapse/setup.sh, bash -c "$(curl …)", etc).
# `${BASH_SOURCE[0]}` is the path of the file currently executing.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly INSTALLER_TEMPLATES="$HERE/installer/templates"
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
    --upgrade                Run the upgrade flow (git pull + compose
                             pull + restart). NOT YET IMPLEMENTED.
    --doctor                 Run preflight + status checks against an
                             existing install; no mutations.
    --uninstall              Remove the install + containers; preserves
                             volumes by default. NOT YET IMPLEMENTED.
    --install-dir=<path>     Override $INSTALL_DIR_DEFAULT.
    --version                Print installer version and exit.
    --help                   This message.

Environment:
    SYNAPSE_NON_INTERACTIVE  Same as --non-interactive (any non-empty)
    SYNAPSE_INSTALL_LOG      Override log path (default $LOG_FILE)
EOF
}

# CLI parsing --------------------------------------------------------

parse_flags() {
    DOMAIN=""
    ACME_EMAIL=""
    ENABLE_HA=0
    NO_TLS=0
    SKIP_DNS=0
    DOCTOR=0
    UPGRADE=0
    UNINSTALL=0
    INSTALL_DIR="$INSTALL_DIR_DEFAULT"
    while (( $# > 0 )); do
        case "$1" in
            --domain=*)        DOMAIN="${1#*=}" ;;
            --acme-email=*)    ACME_EMAIL="${1#*=}" ;;
            --enable-ha)       ENABLE_HA=1 ;;
            --no-tls)          NO_TLS=1 ;;
            --skip-dns-check)  SKIP_DNS=1 ;;
            --non-interactive) export SYNAPSE_NON_INTERACTIVE=1 ;;
            --upgrade)         UPGRADE=1 ;;
            --doctor)          DOCTOR=1 ;;
            --uninstall)       UNINSTALL=1 ;;
            --install-dir=*)   INSTALL_DIR="${1#*=}" ;;
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
}

phase_preflight() {
    CURRENT_STEP="preflight"
    ui::step "Pre-flight checks"
    local domain_arg="$DOMAIN"
    if (( SKIP_DNS )); then
        domain_arg=""
    fi
    if ! preflight::run_all "$domain_arg"; then
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
        export SYNAPSE_PUBLIC_URL="${DOMAIN:+https://$DOMAIN}"
        export SYNAPSE_ALLOWED_ORIGINS="${DOMAIN:+https://$DOMAIN}"
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
    local verify_args=(--keep-demo)
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
    local public_url="${DOMAIN:+https://$DOMAIN}"
    public_url="${public_url:-http://localhost:${SYNAPSE_PORT:-8080}}"
    cat <<EOF

✓ Synapse is ready.

  URL: $public_url
  Install dir: $INSTALL_DIR
  Log: $LOG_FILE

Next steps:
  1. Open the URL above and register your admin user
  2. Create a team → project → deployment
  3. Use the CLI snippet from the deployment row to run \`npx convex deploy\`

Useful commands:
  $INSTALL_DIR/setup.sh --doctor       — re-run health checks
  $INSTALL_DIR/setup.sh --upgrade      — pull the latest version (TODO)
  docker compose -f $INSTALL_DIR/docker-compose.yml logs -f synapse

EOF
}

# main ----------------------------------------------------------------

main() {
    parse_flags "$@"

    # Doctor mode — run preflight against the existing install and
    # exit. No mutations.
    if (( DOCTOR )); then
        source_libs
        ui::step "Synapse installer ${INSTALLER_VERSION} — doctor mode"
        preflight::run_all "$DOMAIN"
        exit $?
    fi

    if (( UPGRADE )); then
        echo "--upgrade is not yet implemented (v0.6.1)" >&2
        exit 2
    fi
    if (( UNINSTALL )); then
        echo "--uninstall is not yet implemented (v0.6.1)" >&2
        exit 2
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
    phase_preflight
    phase_install_deps
    phase_install_dir
    phase_secrets
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
