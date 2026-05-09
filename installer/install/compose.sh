# installer/install/compose.sh
# shellcheck shell=bash
#
# Docker-compose lifecycle helpers — pull, up, wait_healthy, down.
# Composes the ui::* helpers from chunk 2 so each long-running call
# gets a spinner. The actual `docker compose` invocations are thin
# wrappers; we don't try to abstract over the CLI.
#
# Tests inject COMPOSE_CMD=/path/to/fake-docker so the docker calls
# are mockable, and COMPOSE_HEALTH_TIMEOUT_OVERRIDE so the wait loop
# completes in test time.

# compose::pull <compose_dir>
# Pulls the images referenced by the compose file. Idempotent (skips
# layers that are up to date).
compose::pull() {
    local dir="${1:-.}"
    local cmd="${COMPOSE_CMD:-docker}"
    ui::spin "Pulling Synapse images" \
        "$cmd" compose -f "$dir/docker-compose.yml" pull
}

# compose::up <compose_dir> [--profile name]... [--build] [extra args]
# Brings the stack up in detached mode. --profile flags accumulate
# (the v0.6 caddy + ha profiles can be combined). Other unknown
# args pass through verbatim to `docker compose up` — this lets
# callers add `--build` (which builds locally-defined services like
# `synapse` and `dashboard` instead of trying to pull them, since
# they have no published image) without us having to whitelist
# every compose flag.
compose::up() {
    local dir="${1:-.}"
    shift
    local cmd="${COMPOSE_CMD:-docker}"
    local profiles=() up_args=()
    while (( $# > 0 )); do
        case "$1" in
            --profile)
                profiles+=(--profile "$2")
                shift 2
                ;;
            *)
                up_args+=("$1")
                shift
                ;;
        esac
    done
    # When the caller passes --build (every upgrade and every fresh
    # install), also pass --force-recreate. compose's recreate decision
    # uses the per-service config-hash label on the running container,
    # NOT the image SHA. A fresh `docker build` that produces a new
    # image SHA at the SAME tag (synapse:local / synapse-dashboard:local)
    # leaves the config-hash unchanged → compose marks the container
    # as up-to-date → operator keeps running yesterday's binary even
    # though a brand-new image now exists. v1.5.1 → v1.5.3 hit exactly
    # this trap on real VPS — the dashboard chip stayed yellow because
    # the dashboard container was never recreated. --force-recreate
    # bypasses the hash check and recreates every container in the
    # project, guaranteeing the new image is what actually runs.
    # Cost: ~5-15s per upgrade (postgres + caddy briefly cycle); the
    # health-wait already absorbs that window. Belt: skip when --build
    # is absent so `lifecycle::rollback_images` (which calls compose up
    # without --build) keeps its non-disruptive semantics.
    local has_build=0
    local arg
    for arg in "${up_args[@]}"; do
        if [[ "$arg" == "--build" ]]; then
            has_build=1
            break
        fi
    done
    if (( has_build )); then
        up_args+=(--force-recreate)
    fi
    local args=(compose -f "$dir/docker-compose.yml" "${profiles[@]}" up -d "${up_args[@]}")
    ui::spin "Bringing up the stack" "$cmd" "${args[@]}"
}

# compose::down <compose_dir>
# Stops + removes the stack. Used by uninstall.sh and on failure
# rollback. Volumes are preserved by default; --volumes removes them
# (callers wanting a destructive rollback pass that explicitly).
compose::down() {
    local dir="${1:-.}"
    shift || true
    local cmd="${COMPOSE_CMD:-docker}"
    local args=(compose -f "$dir/docker-compose.yml" down)
    while (( $# > 0 )); do
        case "$1" in
            --volumes) args+=(--volumes) ;;
        esac
        shift
    done
    "$cmd" "${args[@]}"
}

# compose::wait_healthy <url> [timeout_seconds=60]
# Polls a health URL until it returns 2xx (curl -sf), or until
# timeout. Returns 0 on healthy, 1 on timeout. Uses curl --max-time
# 2 per attempt so a slow / hung backend doesn't blow the budget.
#
# Tests can shorten via COMPOSE_HEALTH_TIMEOUT_OVERRIDE so the
# 60-second default doesn't drag the suite.
compose::wait_healthy() {
    local url="${1:-}"
    [[ -z "$url" ]] && { echo "compose::wait_healthy: url required" >&2; return 2; }
    local timeout="${COMPOSE_HEALTH_TIMEOUT_OVERRIDE:-${2:-60}}"
    local elapsed=0
    local cmd="${COMPOSE_CURL:-curl}"
    while (( elapsed < timeout )); do
        if "$cmd" -sf -o /dev/null --max-time 2 "$url" 2>/dev/null; then
            return 0
        fi
        sleep 1
        elapsed=$(( elapsed + 1 ))
    done
    return 1
}
