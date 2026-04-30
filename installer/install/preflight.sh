# installer/install/preflight.sh
# shellcheck shell=bash
#
# Preflight checks — runs through "is this VPS suitable for Synapse?"
# in a single pass. Each check returns:
#   0  pass  — emits ui::success and continues
#   1  warn  — emits ui::warn and continues (recoverable: maybe offer
#              to fix, maybe just nag)
#   2  fail  — emits ui::fail; preflight::run_all aborts after the
#              full pass so the operator sees every failing check at
#              once instead of one-at-a-time whack-a-mole
#
# Composes the detect:: helpers from installer/lib/detect.sh and the
# ui:: helpers from installer/install/ui.sh. setup.sh is responsible
# for sourcing both BEFORE this file.
#
# Tests stub the detect:: / ui:: helpers via shell-function
# redefinition (after sourcing) and mock external commands (docker,
# dig, curl) via the PATH-shadow helper from
# installer/test/helpers/load.bash.

# preflight::check_os — Linux is the only supported family; within
# Linux we tier:
#   debian / redhat        → green: tested on every release
#   arch / alpine / suse    → yellow: should work, community-tested
#   anything else (BSD,…)   → red: refuse to install
preflight::check_os() {
    local id family version
    id="$(detect::os_id)"
    family="$(detect::os_family)"
    version="$(detect::os_version)"
    case "$family" in
        debian|redhat)
            ui::success "OS: $id $version"
            return 0
            ;;
        arch|alpine|suse)
            ui::warn "OS: $id $version (family $family) — community-tested only"
            return 1
            ;;
        *)
            ui::fail "OS: $id is not supported. v0.6 needs Debian/Ubuntu/Fedora/RHEL family."
            return 2
            ;;
    esac
}

# preflight::check_arch — amd64 and arm64 are the only architectures
# the Convex backend image is built for. armv7 and i386 fail fast so
# the operator doesn't burn 5 minutes on a `docker pull` that ends in
# "no matching manifest".
preflight::check_arch() {
    local arch
    arch="$(detect::arch)"
    case "$arch" in
        amd64|arm64)
            ui::success "Architecture: $arch"
            return 0
            ;;
        *)
            ui::fail "Architecture: $arch — only amd64 and arm64 are supported."
            return 2
            ;;
    esac
}

# preflight::check_sudo — use detect::sudo_cmd to probe root/sudo/doas
# in one go. We DO NOT actually invoke sudo here (no `sudo -v`); that
# would prompt mid-preflight which is rude. The check just verifies a
# working escalation path *exists*. setup.sh will hit the prompt later,
# at which point the operator is already mentally committed.
preflight::check_sudo() {
    local cmd
    if cmd="$(detect::sudo_cmd)"; then
        if [[ -z "$cmd" ]]; then
            ui::success "Privileges: running as root"
        else
            ui::success "Privileges: $cmd available"
        fi
        return 0
    fi
    ui::fail "Privileges: not root and no sudo/doas on PATH"
    return 2
}

# preflight::check_docker — three states:
#   absent       → warn + remediation hint (offer to install in setup.sh)
#   too old      → fail (we need 20.10+ for the modern compose plugin)
#   present, OK  → success
#
# The version compare uses `sort -V` rather than parsing semvers
# because Docker's version string occasionally has odd suffixes
# ("24.0.7+ce-debian", "20.10.21~3-0~ubuntu-jammy"). sort -V handles
# them; a homemade major.minor parser doesn't.
preflight::check_docker() {
    if ! detect::has_docker; then
        ui::warn "Docker: not installed"
        ui::info "  Install: curl -fsSL https://get.docker.com | sh"
        return 1
    fi
    if ! docker info >/dev/null 2>&1; then
        ui::fail "Docker: installed but daemon unreachable (try: sudo systemctl start docker)"
        return 2
    fi
    local version min="20.10"
    version="$(docker version --format '{{.Server.Version}}' 2>/dev/null || echo 0)"
    if [[ "$(printf '%s\n%s\n' "$min" "$version" | sort -V | head -n1)" != "$min" ]]; then
        ui::fail "Docker: $version detected, $min+ required"
        return 2
    fi
    ui::success "Docker: $version"
    return 0
}

# preflight::check_compose — the modern compose plugin (v2) ships with
# Docker Desktop and current docker.io. The legacy `docker-compose`
# Python tool (v1) is intentionally NOT supported — its yaml parser
# diverges and the new compose features in our docker-compose.yml
# aren't backported.
preflight::check_compose() {
    if ! docker compose version >/dev/null 2>&1; then
        ui::fail "Docker Compose v2: not available (legacy 'docker-compose' v1 is not supported)"
        ui::info "  Install: sudo apt-get install -y docker-compose-plugin"
        return 2
    fi
    local v
    v="$(docker compose version --short 2>/dev/null || echo present)"
    ui::success "Docker Compose v2: $v"
    return 0
}

# preflight::check_disk — by default Synapse needs ~10GB on the
# filesystem holding /var/lib/docker (image + per-deployment volumes).
# Operators on a tight VPS can override via SYNAPSE_DISK_GB_MIN.
preflight::check_disk() {
    local need="${SYNAPSE_DISK_GB_MIN:-10}"
    local free
    free="$(detect::disk_free_gb /)" || true
    if (( free >= need )); then
        ui::success "Disk: $free GB free (need >= $need)"
        return 0
    fi
    ui::fail "Disk: $free GB free, need at least $need GB on /"
    return 2
}

# preflight::check_ram — 2GB is enough for the control plane plus a
# couple of small Convex backends. <1GB fails outright; 1-2GB warns
# (Synapse will run, but provisioning multiple deployments will OOM).
preflight::check_ram() {
    local need="${SYNAPSE_RAM_GB_MIN:-2}"
    local total
    total="$(detect::ram_total_gb)" || true
    if (( total >= need )); then
        ui::success "RAM: $total GB total (need >= $need)"
        return 0
    fi
    if (( total >= 1 )); then
        ui::warn "RAM: $total GB — Synapse will run but provisioning will be tight (need >= $need)"
        return 1
    fi
    ui::fail "RAM: $total GB total, need at least $need GB"
    return 2
}

# preflight::check_outbound — the operator's first deployment-create
# is going to pull ghcr.io/get-convex/convex-backend (~150MB). If the
# VPS firewall blocks ghcr.io we want to know now, not when the user
# is staring at a stuck spinner.
preflight::check_outbound() {
    if ! detect::has_cmd curl; then
        ui::warn "Outbound: curl not installed; can't verify image registry reachability"
        return 1
    fi
    if curl -sf --max-time 5 -o /dev/null https://ghcr.io 2>/dev/null; then
        ui::success "Outbound: ghcr.io reachable"
        return 0
    fi
    ui::warn "Outbound: ghcr.io not reachable in 5s — first deployment will fail"
    return 1
}

# preflight::check_dns <domain>
# Resolves the domain, compares to the host's public IP. Mismatch is a
# warn rather than a fail because the operator may be running setup.sh
# *before* DNS has propagated, and we want them to be able to proceed
# with --skip-dns-check (or just by ignoring the warning) instead of
# being blocked.
preflight::check_dns() {
    local domain="${1:-}"
    if [[ -z "$domain" ]]; then
        return 0
    fi
    if ! detect::has_cmd dig; then
        ui::warn "DNS: 'dig' not installed; skipping A-record check for $domain"
        ui::info "  Install: sudo apt-get install -y dnsutils  # or bind-utils on RHEL"
        return 1
    fi
    if ! detect::has_cmd curl; then
        ui::warn "DNS: 'curl' not installed; can't compare against public IP"
        return 1
    fi
    local resolved my_ip
    resolved="$(dig +short "$domain" 2>/dev/null | head -n1)"
    my_ip="$(curl -sf --max-time 5 https://api.ipify.org 2>/dev/null || echo "")"
    if [[ -z "$resolved" ]]; then
        ui::warn "DNS: $domain has no A record (hasn't propagated yet?)"
        return 1
    fi
    if [[ -z "$my_ip" ]]; then
        ui::warn "DNS: can't determine this host's public IP; resolved $domain → $resolved"
        return 1
    fi
    if [[ "$resolved" == "$my_ip" ]]; then
        ui::success "DNS: $domain → $resolved (matches this host)"
        return 0
    fi
    ui::warn "DNS: $domain → $resolved, but this host is $my_ip (Caddy can't get a cert until they match)"
    return 1
}

# preflight::run_all [domain]
# Runs every check in order. Returns:
#   0 — all pass (or pass+warn)
#   2 — at least one fail (caller should abort)
#
# We always run the FULL list even after a fail so the operator gets
# every issue in a single pass instead of fix-rerun-fix-rerun. The
# summary line at the end is the single source of truth for "is this
# VPS ready?".
preflight::run_all() {
    local domain="${1:-}"
    local fails=0 warns=0
    local checks=(check_os check_arch check_sudo check_docker check_compose check_disk check_ram check_outbound)
    [[ -n "$domain" ]] && checks+=(check_dns)
    local check rc
    for check in "${checks[@]}"; do
        if [[ "$check" == "check_dns" ]]; then
            preflight::"$check" "$domain"
        else
            preflight::"$check"
        fi
        rc=$?
        case "$rc" in
            0) ;;
            1) warns=$((warns + 1)) ;;
            2) fails=$((fails + 1)) ;;
            *) fails=$((fails + 1)) ;;  # treat unexpected as fail
        esac
    done
    echo
    if (( fails > 0 )); then
        ui::fail "Preflight: $fails fail / $warns warn — fix the issues above and re-run"
        return 2
    fi
    if (( warns > 0 )); then
        ui::warn "Preflight: $warns warn — proceeding (set --strict to abort on warns)"
        return 0
    fi
    ui::success "Preflight: all checks passed"
    return 0
}
