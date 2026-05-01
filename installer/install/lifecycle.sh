# installer/install/lifecycle.sh
# shellcheck shell=bash
#
# Lifecycle commands for an existing Synapse install. v0.6.1 ships
# `lifecycle::upgrade`; --backup/--restore/--uninstall/--logs/--status
# follow as their own PRs. Each function is invoked from setup.sh
# AFTER an existing install is detected (via $INSTALL_DIR/.env +
# docker-compose.yml). The function owns its own user-visible UI,
# version-detection, and audit trail in $INSTALL_DIR/upgrade.log.
#
# Conventions:
#   - All functions return 0 on success, 2 on hard failure (so the
#     setup.sh trap surfaces the right exit code to the operator).
#   - External commands are pinned to env-overridable names
#     (LIFECYCLE_CURL / LIFECYCLE_JQ / LIFECYCLE_GIT / COMPOSE_CMD)
#     so bats can PATH-shadow them with mocks.
#   - We use `secrets::env_get` / `secrets::set_env_var` for every
#     .env read/write — the SYNAPSE_VERSION stamp is an existing
#     value that must be force-overwritten on upgrade, not just
#     filled-when-empty.

# Defaults are env-overridable so tests can point at a fake API and
# tags don't have to round-trip through GitHub during CI.
LIFECYCLE_REPO_URL="${LIFECYCLE_REPO_URL:-https://github.com/Iann29/convex-synapse.git}"
LIFECYCLE_GITHUB_API="${LIFECYCLE_GITHUB_API:-https://api.github.com}"
LIFECYCLE_REPO_SLUG="${LIFECYCLE_REPO_SLUG:-Iann29/convex-synapse}"
LIFECYCLE_HEALTH_TIMEOUT="${LIFECYCLE_HEALTH_TIMEOUT:-180}"

# ---- version resolution --------------------------------------------

# lifecycle::resolve_target_ref [<override>]
# Echoes the git ref to fetch. Priority:
#   1. explicit override (operator passed --ref=X) — used verbatim
#   2. GitHub Releases /latest tag_name (auth-less public API)
#   3. fallback to "main"
#
# A 5-second timeout on the API call keeps the upgrade snappy when
# api.github.com is unreachable; we don't want the operator staring
# at a hung "checking version" prompt for 30 seconds before we tell
# them we're going to use main anyway.
lifecycle::resolve_target_ref() {
    local override="${1:-}"
    if [[ -n "$override" ]]; then
        printf '%s' "$override"
        return 0
    fi
    local curl_cmd="${LIFECYCLE_CURL:-curl}"
    local jq_cmd="${LIFECYCLE_JQ:-jq}"
    local body
    if body="$("$curl_cmd" -sf --max-time 5 \
            "$LIFECYCLE_GITHUB_API/repos/$LIFECYCLE_REPO_SLUG/releases/latest" \
            2>/dev/null)"; then
        local tag
        tag="$(printf '%s' "$body" | "$jq_cmd" -r '.tag_name // empty' 2>/dev/null || true)"
        if [[ -n "$tag" ]]; then
            printf '%s' "$tag"
            return 0
        fi
    fi
    printf '%s' "main"
}

# lifecycle::current_version <env_file>
# Reads SYNAPSE_VERSION from the existing .env, or prints empty if the
# stamp is missing (older installs that pre-date the stamp).
lifecycle::current_version() {
    local env_file="$1"
    secrets::env_get "$env_file" SYNAPSE_VERSION
}

# ---- image snapshot + rollback -------------------------------------

# lifecycle::snapshot_images <compose_dir> <out_file>
# Records (service<TAB>repo:tag<TAB>image_id) for each compose service
# so a failed upgrade can be rolled back by re-tagging the original
# image IDs to the same repo:tag refs the compose file expects.
#
# Why image_id rather than digest: the synapse + dashboard services
# are `build:` services with no registry digest. The image ID
# (sha256:...) is the only stable handle on the locally-built image.
# `docker tag <id> <repo>:<tag>` re-points the tag back even after
# `up --build` has written a new image at the same tag.
lifecycle::snapshot_images() {
    local dir="$1" out="$2"
    local cmd="${COMPOSE_CMD:-docker}"
    local jq_cmd="${LIFECYCLE_JQ:-jq}"
    : >"$out"
    local jq_filter='.[] | select(.Repository != "" and .ID != "") |
        [.Service, (.Repository + ":" + (.Tag // "latest")), .ID] | @tsv'
    "$cmd" compose -f "$dir/docker-compose.yml" images --format json 2>/dev/null \
        | "$jq_cmd" -r "$jq_filter" >"$out" 2>/dev/null || true
}

# lifecycle::rollback_images <snapshot_file> <compose_dir>
# Best-effort: re-tag every image in the snapshot back to its repo:tag,
# then `compose up -d` (without --build) so the project picks up the
# restored images. We swallow individual errors so a single missing
# image (e.g. operator pruned it between snapshot and rollback) doesn't
# prevent rollback of the rest.
lifecycle::rollback_images() {
    local snap="$1" dir="$2"
    if [[ ! -s "$snap" ]]; then
        return 1
    fi
    local cmd="${COMPOSE_CMD:-docker}"
    while IFS=$'\t' read -r _service repo_tag image_id; do
        if [[ -n "$image_id" && -n "$repo_tag" ]]; then
            "$cmd" tag "$image_id" "$repo_tag" 2>/dev/null || true
        fi
    done < "$snap"
    "$cmd" compose -f "$dir/docker-compose.yml" up -d 2>/dev/null || true
}

# ---- audit trail ---------------------------------------------------

# lifecycle::log <log_file> <message...>
# Append an ISO-8601 timestamped line to the install dir's upgrade.log.
# Best-effort — never fails the caller. The log is operator-visible and
# meant to be the first thing they `tail` when an upgrade goes sideways.
lifecycle::log() {
    local log_file="$1"
    shift
    {
        printf '[%s] %s\n' "$(date -Iseconds 2>/dev/null || date)" "$*"
    } >>"$log_file" 2>/dev/null || true
}

# ---- profile detection ---------------------------------------------

# lifecycle::detect_profiles <env_file>
# Echoes the --profile flags the original install enabled, one
# argument per line. Today: caddy (when a standalone Caddyfile sits
# in the install dir) and ha (when SYNAPSE_HA_ENABLED=true). Read by
# `lifecycle::upgrade` so the rebuild brings the same services up.
lifecycle::detect_profiles() {
    local env_file="$1"
    if [[ -f "$(dirname "$env_file")/Caddyfile" ]]; then
        printf -- '--profile\ncaddy\n'
    fi
    local ha
    ha="$(secrets::env_get "$env_file" SYNAPSE_HA_ENABLED)"
    if [[ "$ha" == "true" ]]; then
        printf -- '--profile\nha\n'
    fi
}

# ---- main entry point ----------------------------------------------

# lifecycle::upgrade <install_dir> [--ref=<ref>] [--force]
# Top-level upgrade flow. Returns 0 on success, 2 on failure.
#
# Flow:
#   1. Validate install_dir has .env + docker-compose.yml
#   2. Resolve target ref (explicit --ref > GitHub releases /latest > main)
#   3. Skip if current == target (unless --force, or target == main)
#   4. Snapshot current image IDs for rollback
#   5. git clone --depth=1 --branch=<target> into a temp dir
#   6. rsync new tree into install_dir, preserving .env + Caddyfile +
#      upgrade.log + .upgrade-snapshot.tsv
#   7. docker pull external images (best-effort)
#   8. compose up -d --build with original profile flags
#   9. wait_healthy on /health (LIFECYCLE_HEALTH_TIMEOUT seconds)
#  10. On any failure between 5-9: rollback_images and exit 2
#  11. Stamp new SYNAPSE_VERSION into .env on success
# Public entry point — wraps `_upgrade_inner` with deterministic
# cleanup. We can't `trap RETURN` for the temp clone dir because
# RETURN traps in bash fire on EVERY function return inside the
# trap-setting function (including ui::spin, snapshot_images, ...) —
# the clone dir would be wiped before rsync ever runs. Wrapping the
# inner logic in its own function keeps cleanup atomic.
lifecycle::upgrade() {
    # _path suffix avoids the dynamic-scope shadow with the local
    # `tmp_clone` inside _upgrade_inner — printf -v from the inner
    # would otherwise write to the inner's local instead of ours.
    local _tmp_clone_path=""
    local rc=0
    lifecycle::_upgrade_inner "$@" tmp_clone_var=_tmp_clone_path || rc=$?
    if [[ -n "$_tmp_clone_path" && -d "$_tmp_clone_path" ]]; then
        rm -rf "$_tmp_clone_path"
    fi
    return $rc
}

lifecycle::_upgrade_inner() {
    local install_dir="$1"
    shift
    local ref="" force=0
    # tmp_clone_var is the name of a variable in the OUTER scope we
    # should populate so the cleanup wrapper knows what to remove.
    local tmp_clone_var=""
    while (( $# > 0 )); do
        case "$1" in
            --ref=*) ref="${1#*=}" ;;
            --ref)   ref="${2:-}"; shift ;;
            --force) force=1 ;;
            tmp_clone_var=*) tmp_clone_var="${1#*=}" ;;
        esac
        shift
    done

    local env_file="$install_dir/.env"
    local compose_file="$install_dir/docker-compose.yml"
    local log_file="$install_dir/upgrade.log"
    local snap_file="$install_dir/.upgrade-snapshot.tsv"

    # --- 1. validate -----------------------------------------------
    if [[ ! -f "$env_file" ]]; then
        ui::fail "no .env at $env_file — is this a Synapse install dir?"
        ui::info "Run setup.sh without --upgrade to install fresh."
        return 2
    fi
    if [[ ! -f "$compose_file" ]]; then
        ui::fail "no docker-compose.yml at $compose_file — corrupted install?"
        return 2
    fi

    # --- 2. resolve target ref -------------------------------------
    local target current
    target="$(lifecycle::resolve_target_ref "$ref")"
    current="$(lifecycle::current_version "$env_file")"

    ui::step "Synapse upgrade"
    ui::info "Install dir: $install_dir"
    ui::info "Current version: ${current:-unknown}"
    ui::info "Target ref: $target"

    # --- 3. skip if already on latest ------------------------------
    # Strip leading "v" so "v0.6.1" matches the bare "0.6.1" we stamp
    # in .env. Branch refs (main, develop) are moving targets, never
    # short-circuit those — operator chasing main shouldn't need
    # --force every run.
    local stamp_target="${target#v}"
    local is_branch=0
    if [[ "$target" == "main" || "$target" == "develop" ]]; then
        is_branch=1
    fi
    if (( ! is_branch )) && [[ "$stamp_target" == "$current" ]] && (( ! force )); then
        ui::success "Already on $current — pass --force to re-run anyway"
        return 0
    fi

    lifecycle::log "$log_file" "upgrade start: ${current:-unknown} → $target"

    # --- 4. snapshot current images --------------------------------
    ui::spin "Snapshotting current image tags" \
        lifecycle::snapshot_images "$install_dir" "$snap_file"

    # --- 5. fetch new code -----------------------------------------
    local tmp_clone
    tmp_clone="$(mktemp -d 2>/dev/null || mktemp -d -t synapse-upgrade)"
    # Hand the path back to the wrapper so it can rm -rf on return.
    if [[ -n "$tmp_clone_var" ]]; then
        # shellcheck disable=SC2086  # tmp_clone_var holds a name, not a value.
        printf -v "$tmp_clone_var" '%s' "$tmp_clone"
    fi

    local git_cmd="${LIFECYCLE_GIT:-git}"
    if ! ui::spin "Cloning $LIFECYCLE_REPO_URL @ $target" \
            "$git_cmd" clone --depth=1 --branch="$target" \
                "$LIFECYCLE_REPO_URL" "$tmp_clone"; then
        ui::fail "git clone failed — check network / branch / tag exists"
        lifecycle::log "$log_file" "upgrade failed: clone $target"
        return 2
    fi

    # --- 6. sync into install_dir ----------------------------------
    # Excludes preserve operator state. Notably .env (secrets), the
    # rendered Caddyfile (may have manual edits), upgrade.log
    # (history), and the snapshot file we just wrote (otherwise
    # rollback after sync wouldn't work). We deliberately do NOT pass
    # --delete: leftover files from the previous version are harmless
    # and the safety win is worth more than the tidiness.
    local prefix=""
    prefix="$(detect::sudo_cmd 2>/dev/null || true)"
    if detect::has_cmd rsync; then
        $prefix rsync -a \
            --exclude='.git' \
            --exclude='node_modules' \
            --exclude='.env' \
            --exclude='Caddyfile' \
            --exclude='upgrade.log' \
            --exclude='.upgrade-snapshot.tsv' \
            "$tmp_clone/" "$install_dir/"
    else
        ui::warn "rsync not found — falling back to cp -a (no exclusions)"
        $prefix cp -a "$tmp_clone/." "$install_dir/"
    fi

    # --- 7. pre-pull external images -------------------------------
    # Same logic as phase_compose_up: ensure the convex-backend +
    # convex-dashboard images exist locally before compose up tries
    # them. Best-effort; build will surface any pull failure.
    local docker_cmd="${COMPOSE_CMD:-docker}"
    local backend_image dashboard_image
    backend_image="$(secrets::env_get "$env_file" SYNAPSE_BACKEND_IMAGE)"
    backend_image="${backend_image:-ghcr.io/get-convex/convex-backend:latest}"
    dashboard_image="ghcr.io/get-convex/convex-dashboard:latest"
    "$docker_cmd" pull "$backend_image" >/dev/null 2>&1 || true
    "$docker_cmd" pull "$dashboard_image" >/dev/null 2>&1 || true

    # --- 8. compose up -d --build ----------------------------------
    local profile_args=()
    while IFS= read -r line; do
        if [[ -n "$line" ]]; then
            profile_args+=("$line")
        fi
    done < <(lifecycle::detect_profiles "$env_file")

    if ! compose::up "$install_dir" "${profile_args[@]}" --build; then
        ui::fail "docker compose up --build failed"
        lifecycle::log "$log_file" "upgrade failed: build"
        lifecycle::_rollback "$install_dir" "$snap_file" "$log_file"
        return 2
    fi

    # --- 9. wait for /health ---------------------------------------
    local synapse_port
    synapse_port="$(secrets::env_get "$env_file" SYNAPSE_PORT)"
    synapse_port="${synapse_port:-8080}"
    local health_url="http://localhost:$synapse_port/health"
    if ! compose::wait_healthy "$health_url" "$LIFECYCLE_HEALTH_TIMEOUT"; then
        ui::fail "Synapse didn't become healthy in ${LIFECYCLE_HEALTH_TIMEOUT}s after upgrade"
        lifecycle::log "$log_file" "upgrade failed: health"
        lifecycle::_rollback "$install_dir" "$snap_file" "$log_file"
        return 2
    fi

    # --- 11. stamp new version ------------------------------------
    # `set_env_var` force-overwrites; `ensure_env_var` would no-op
    # because SYNAPSE_VERSION already has a value (it's the WHOLE
    # POINT of the stamp).
    local new_stamp
    if (( is_branch )); then
        new_stamp="$target"
    else
        new_stamp="$stamp_target"
    fi
    secrets::set_env_var "$env_file" SYNAPSE_VERSION "$new_stamp"

    ui::success "Upgrade complete: ${current:-unknown} → $new_stamp"
    lifecycle::log "$log_file" "upgrade success: ${current:-unknown} → $new_stamp"
    return 0
}

# lifecycle::_rollback <install_dir> <snap_file> <log_file>
# Internal helper: invoked when build or health check fails after
# the source has already been swapped in. We re-tag the previous
# images from the snapshot and bring the stack up without --build.
# The new source code stays in $install_dir (a follow-up
# --backup/--restore in v0.6.1+ owns full source rollback); the
# operator gets a clear log line + the snapshot path printed so
# they can recover by hand if needed.
lifecycle::_rollback() {
    local install_dir="$1" snap="$2" log="$3"
    ui::warn "Rolling back to previous images"
    if ! lifecycle::rollback_images "$snap" "$install_dir"; then
        ui::warn "no snapshot found at $snap — nothing to roll back"
        lifecycle::log "$log" "rollback: no snapshot"
        return 1
    fi
    lifecycle::log "$log" "rollback: re-tagged from $snap"
    ui::warn "Rollback applied — inspect with:"
    ui::warn "  docker compose -f $install_dir/docker-compose.yml logs --tail=200"
}
