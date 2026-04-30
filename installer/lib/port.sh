# installer/lib/port.sh
# shellcheck shell=bash
#
# TCP-port helpers used by the v0.6 installer's preflight + compose
# rendering phases. Single-machine assumption — we only ever look at
# IPv4 + IPv6 listening sockets on the current host.
#
# Source from a caller that has set `set -euo pipefail`. Helpers do not
# call `set` themselves; they live happily inside `if`/`$(...)`/
# `port::in_use $p || …` constructions.
#
# Dependency: `ss` (from iproute2). It's pre-installed on every modern
# distro we target. We deliberately don't fall back to `netstat` or
# `lsof` — fewer paths to test, clearer error if the host is missing
# the toolchain.

# port::in_use <port> — Exits 0 when something is listening on that TCP
# port (IPv4 or IPv6), 1 when the port is free, 2 on usage error.
#
# Notes on the ss invocation:
#   -H  no header row, so wc counts only matches
#   -l  listening sockets only — we don't care about ESTABLISHED
#   -t  TCP only (Synapse + Convex backends + Postgres are all TCP)
#   -n  numeric, no DNS lookups (faster, deterministic for tests)
#   ( sport = :PORT )  ss filter syntax — exact source-port match
port::in_use() {
    local port="${1:-}"
    if [[ -z "$port" ]] || ! [[ "$port" =~ ^[0-9]+$ ]]; then
        echo "port::in_use: numeric port required" >&2
        return 2
    fi
    if (( port < 1 || port > 65535 )); then
        echo "port::in_use: $port is out of range (1..65535)" >&2
        return 2
    fi
    if ! command -v ss >/dev/null 2>&1; then
        echo "port::in_use: 'ss' not found (install iproute2)" >&2
        return 2
    fi
    local count
    count="$(ss -ltnH "( sport = :$port )" 2>/dev/null | wc -l)"
    (( count > 0 ))
}

# port::find_free <start> [end] — Echoes the first free TCP port at or
# above `start`, scanning through `end` (default 65535). Returns 1 if
# nothing free in the range. Used to auto-pick replacement ports when
# the operator's defaults clash with whatever else they have running
# (very common on the scopuli-style "one VPS, many services" host).
#
# Capped at 1000 attempts to bound the runtime; in practice we look at
# a contiguous block of ~5 ports around the default and return on the
# first miss.
port::find_free() {
    local start="${1:-}"
    local end="${2:-65535}"
    if [[ -z "$start" ]] || ! [[ "$start" =~ ^[0-9]+$ ]]; then
        echo "port::find_free: numeric start port required" >&2
        return 2
    fi
    if ! [[ "$end" =~ ^[0-9]+$ ]]; then
        echo "port::find_free: end must be numeric" >&2
        return 2
    fi
    if (( start < 1 || start > 65535 || end < start || end > 65535 )); then
        echo "port::find_free: invalid range $start..$end" >&2
        return 2
    fi
    local port="$start" tries=0
    local -r MAX_TRIES=1000
    while (( port <= end && tries < MAX_TRIES )); do
        # in_use returns 2 on usage error — propagate so we don't loop
        # forever pretending every port is free.
        if port::in_use "$port"; then
            : # taken, keep searching
        else
            local rc=$?
            if (( rc == 2 )); then
                return 2
            fi
            echo "$port"
            return 0
        fi
        port=$(( port + 1 ))
        tries=$(( tries + 1 ))
    done
    return 1
}
