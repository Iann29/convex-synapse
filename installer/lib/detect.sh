# installer/lib/detect.sh
# shellcheck shell=bash
#
# Pure-bash detection helpers used by setup.sh and the v0.6 phase
# scripts. Every function is read-only (no side effects), echoes its
# result to stdout, and uses the exit code only to signal "could not
# determine" (1) or hard error (2).
#
# Source from a script that has already set `set -euo pipefail`. The
# helpers do NOT call `set` themselves so they remain composable inside
# `if`/`||`/`$(...)` blocks where errexit semantics differ.
#
# Conventions:
#   - Namespace prefix `detect::` keeps these from colliding with the
#     caller's globals (Tailscale-style, but explicit).
#   - Every function returns 0 on success and a non-zero code on
#     failure. "Failure" means we couldn't gather the info; the caller
#     decides whether that's fatal.
#   - Tests inject overrides via env vars (DETECT_OS_RELEASE) rather
#     than positional args so the call sites stay clean.

# ---- OS / arch ------------------------------------------------------

# detect::os_id — Echoes the `ID=` value from /etc/os-release ("debian",
# "ubuntu", "fedora", "pop", …) or "unknown". Always exits 0 — callers
# branch on the string, not on the exit code.
detect::os_id() {
    local file="${DETECT_OS_RELEASE:-/etc/os-release}"
    if [[ ! -r "$file" ]]; then
        echo "unknown"
        return 0
    fi
    (
        # shellcheck source=/dev/null
        . "$file" 2>/dev/null || true
        echo "${ID:-unknown}"
    )
}

# detect::os_family — Collapses derivatives to a parent family so
# dispatch tables stay small. Output is one of: debian, redhat, arch,
# alpine, suse, unknown. Uses ID first, ID_LIKE as fallback so Pop/Mint/
# Zorin/Linux Mint resolve to "debian" without us listing each one.
detect::os_family() {
    local file="${DETECT_OS_RELEASE:-/etc/os-release}"
    if [[ ! -r "$file" ]]; then
        echo "unknown"
        return 0
    fi
    (
        # shellcheck source=/dev/null
        . "$file" 2>/dev/null || true
        local id="${ID:-}" id_like="${ID_LIKE:-}"
        case "$id" in
            debian|ubuntu) echo debian; return 0 ;;
            fedora|rhel|centos|rocky|almalinux|amzn) echo redhat; return 0 ;;
            arch|manjaro|endeavouros|cachyos) echo arch; return 0 ;;
            alpine) echo alpine; return 0 ;;
            opensuse*|sles|sled) echo suse; return 0 ;;
        esac
        # Fall through to ID_LIKE, which Pop/Mint/etc populate.
        case " $id_like " in
            *" debian "*|*" ubuntu "*) echo debian; return 0 ;;
            *" rhel "*|*" fedora "*|*" centos "*) echo redhat; return 0 ;;
            *" arch "*) echo arch; return 0 ;;
            *" suse "*|*" opensuse "*) echo suse; return 0 ;;
        esac
        echo unknown
    )
}

# detect::os_version — Echoes VERSION_ID (e.g. "12", "24.04") or
# "unknown".
detect::os_version() {
    local file="${DETECT_OS_RELEASE:-/etc/os-release}"
    if [[ ! -r "$file" ]]; then
        echo "unknown"
        return 0
    fi
    (
        # shellcheck source=/dev/null
        . "$file" 2>/dev/null || true
        echo "${VERSION_ID:-unknown}"
    )
}

# detect::os_codename — Echoes VERSION_CODENAME (Debian) or
# UBUNTU_CODENAME (Ubuntu derivatives like Mint that override
# VERSION_CODENAME). Empty string when neither is set.
detect::os_codename() {
    local file="${DETECT_OS_RELEASE:-/etc/os-release}"
    if [[ ! -r "$file" ]]; then
        echo ""
        return 0
    fi
    (
        # shellcheck source=/dev/null
        . "$file" 2>/dev/null || true
        echo "${VERSION_CODENAME:-${UBUNTU_CODENAME:-${DEBIAN_CODENAME:-}}}"
    )
}

# detect::arch — Normalises `uname -m` to the names Docker and most
# package repos use ("amd64", "arm64", "armv7", "i386"). Falls back to
# the raw `uname -m` for anything we don't explicitly map.
detect::arch() {
    local raw
    raw="$(uname -m 2>/dev/null || echo unknown)"
    case "$raw" in
        x86_64|amd64) echo amd64 ;;
        aarch64|arm64) echo arm64 ;;
        armv7l|armv7|armhf) echo armv7 ;;
        i386|i686) echo i386 ;;
        *) echo "$raw" ;;
    esac
}

# ---- Package manager / privilege -----------------------------------

# detect::pkg_manager — Picks the first available package manager so
# the dispatch table can switch on a single string. Order is intentional
# (apt before yum because Debian-derived RHEL clones can have both).
detect::pkg_manager() {
    if command -v apt-get >/dev/null 2>&1; then
        echo apt
    elif command -v dnf >/dev/null 2>&1; then
        echo dnf
    elif command -v yum >/dev/null 2>&1; then
        echo yum
    elif command -v pacman >/dev/null 2>&1; then
        echo pacman
    elif command -v apk >/dev/null 2>&1; then
        echo apk
    elif command -v zypper >/dev/null 2>&1; then
        echo zypper
    else
        echo unknown
    fi
}

# detect::is_root — Exits 0 when running as UID 0. Prefers $EUID for
# speed but falls back to `id -u` so the helper works in shells where
# EUID isn't exported. Tests inject DETECT_UID to bypass bash's
# readonly EUID.
detect::is_root() {
    [[ "${DETECT_UID:-${EUID:-$(id -u)}}" -eq 0 ]]
}

# detect::sudo_cmd — Echoes the prefix the caller should put in front
# of privileged commands: empty when already root, "sudo" when sudo is
# available, "doas" when only doas is, exit 1 + empty stdout otherwise.
# Never executes the command itself.
detect::sudo_cmd() {
    if detect::is_root; then
        echo ""
        return 0
    fi
    if command -v sudo >/dev/null 2>&1; then
        echo sudo
        return 0
    fi
    if command -v doas >/dev/null 2>&1; then
        echo doas
        return 0
    fi
    echo ""
    return 1
}

# ---- Tool presence --------------------------------------------------

# detect::has_cmd <name> — Generic "is this binary on PATH?". Mostly
# used by the more-specific helpers below; exposed because some checks
# in setup.sh need ad-hoc lookups (e.g. `dig`, `openssl`).
detect::has_cmd() {
    command -v "$1" >/dev/null 2>&1
}

detect::has_docker() { command -v docker >/dev/null 2>&1; }
detect::has_caddy()  { command -v caddy  >/dev/null 2>&1; }
detect::has_nginx()  { command -v nginx  >/dev/null 2>&1; }
detect::has_ufw()    { command -v ufw    >/dev/null 2>&1; }

# detect::has_systemd — Per `man systemd`, the canonical detection is
# the existence of /run/systemd/system. Works in containers (returns
# false when systemd isn't PID 1) and on hosts without systemctl.
detect::has_systemd() {
    [[ -d /run/systemd/system ]]
}

# ---- Capacity -------------------------------------------------------

# detect::disk_free_gb [path] — Free GB on the filesystem holding
# `path` (default /). Uses POSIX `df -k` + awk so we don't depend on
# GNU --output. Echoes 0 + exit 1 if df fails (e.g. path doesn't exist).
detect::disk_free_gb() {
    local path="${1:-/}"
    local kb
    kb="$(df -k "$path" 2>/dev/null | awk 'NR==2 {print $4}')"
    if [[ -z "$kb" || ! "$kb" =~ ^[0-9]+$ ]]; then
        echo 0
        return 1
    fi
    # Integer division — round down. A 9.7 GB filesystem reports 9, so
    # a `>= 10 GB` precheck correctly fails. Operators get a clear "you
    # have 9 GB free, need 10" message rather than a silent off-by-one.
    echo "$(( kb / 1024 / 1024 ))"
}

# detect::ram_total_gb — Total RAM in GB from /proc/meminfo. Same
# round-down logic as disk_free_gb. Tests inject a fixture via
# DETECT_MEMINFO; production callers leave it unset.
detect::ram_total_gb() {
    local file="${DETECT_MEMINFO:-/proc/meminfo}"
    local kb
    kb="$(awk '/^MemTotal:/ {print $2}' "$file" 2>/dev/null)"
    if [[ -z "$kb" || ! "$kb" =~ ^[0-9]+$ ]]; then
        echo 0
        return 1
    fi
    echo "$(( kb / 1024 / 1024 ))"
}
