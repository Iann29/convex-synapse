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
    # docker compose images --format json emits .ContainerName (not
    # .Service); fall back to .Service for forward-compat with future
    # compose versions that may rename it.
    local jq_filter='.[] | select(.Repository != "" and .ID != "") |
        [(.ContainerName // .Service // "unknown"),
         (.Repository + ":" + (.Tag // "latest")),
         .ID] | @tsv'
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
    # in .env. Branch refs are moving targets — never short-circuit
    # those, operator chasing main shouldn't need --force every run.
    # Heuristic: anything matching v?X.Y.Z is a tag (semver), anything
    # else (main, develop, feat/foo, fix/bar, ...) is a branch.
    local stamp_target="${target#v}"
    local is_branch=1
    if [[ "$target" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        is_branch=0
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

    # --- 6.5. refresh self-update daemon ---------------------------
    # The new tree on disk includes a possibly-newer synapse-updater
    # binary + systemd unit. We call phase_install_updater so the on-
    # disk copies are refreshed; the function detects
    # SYNAPSE_UPDATER_NO_RESTART (which the daemon sets when it forks
    # this very script) and skips the restart so we don't kill the
    # /status-reporting parent. Best-effort — bare `|| true` because a
    # missing systemd or python on weird hosts shouldn't fail the
    # upgrade itself.
    if declare -F phase_install_updater >/dev/null; then
        phase_install_updater || true
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
    #
    # Slashes are sanitized to '-' because the stamp may flow into
    # contexts that reject them (older docker-compose.yml uses
    # `synapse:${SYNAPSE_VERSION}` as the image tag; an upgrade to
    # ref=feat/foo would fail with "invalid reference format" without
    # this. Modern compose pins the tag to `:local` regardless, but
    # we keep the sanitize as belt-and-suspenders for legacy
    # installs that haven't picked up the new compose yet.)
    local new_stamp
    if (( is_branch )); then
        new_stamp="$target"
    else
        new_stamp="$stamp_target"
    fi
    new_stamp="${new_stamp//\//-}"
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

# ====================================================================
# Backup / Restore (v0.6.1 chunk 2)
# ====================================================================
#
# A backup captures everything an operator needs to rebuild a Synapse
# install from scratch:
#
#   synapse-backup-YYYYMMDD-HHMMSS.tar.gz
#   ├── manifest.json        timestamp, version, volume names, env_included
#   ├── .env                 secrets (operator can pass --exclude-env)
#   ├── docker-compose.yml   the compose file used at backup time
#   ├── synapse.sql.gz       pg_dump --clean --if-exists of metadata DB
#   └── volumes/
#       └── synapse-data-*.tar.gz   one tarball per per-deployment volume
#
# Restore is the reverse: down → wipe + restore volumes → wipe pgdata
# → up postgres → psql in dump → up rest → wait /health.

LIFECYCLE_BACKUP_BUSYBOX_IMAGE="${LIFECYCLE_BACKUP_BUSYBOX_IMAGE:-busybox:stable}"

# lifecycle::backup <install_dir> [--out=<path>] [--exclude-env]
# Returns 0 + prints the archive path on success, 2 on failure.
lifecycle::backup() {
    local _stage_path=""
    local rc=0
    lifecycle::_backup_inner "$@" stage_var=_stage_path || rc=$?
    if [[ -n "$_stage_path" && -d "$_stage_path" ]]; then
        rm -rf "$_stage_path"
    fi
    return $rc
}

lifecycle::_backup_inner() {
    local install_dir="$1"
    shift
    local out_path="" exclude_env=0 stage_var="" to_s3=""
    while (( $# > 0 )); do
        case "$1" in
            --out=*)        out_path="${1#*=}" ;;
            --out)          out_path="${2:-}"; shift ;;
            --exclude-env)  exclude_env=1 ;;
            --to-s3=*)      to_s3="${1#*=}" ;;
            --to-s3)        to_s3="${2:-}"; shift ;;
            stage_var=*)    stage_var="${1#*=}" ;;
        esac
        shift
    done

    # Validate creds + URI BEFORE we spend ~30s bundling a tarball
    # that we won't be able to upload. Failing fast here saves the
    # operator a wasted disk write.
    if [[ -n "$to_s3" ]]; then
        if ! s3::is_s3_uri "$to_s3"; then
            ui::fail "--to-s3 must be an s3:// URI (got: $to_s3)"
            return 2
        fi
        if ! s3::check_creds 2>/dev/null; then
            ui::fail "--to-s3 requires AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY in env"
            ui::info "  export AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... AWS_REGION=us-east-1"
            ui::info "  for S3-compatible (Backblaze, R2, Wasabi, MinIO):"
            ui::info "    export SYNAPSE_BACKUP_S3_ENDPOINT=https://<endpoint>"
            return 2
        fi
    fi

    local env_file="$install_dir/.env"
    local compose_file="$install_dir/docker-compose.yml"
    local backup_log="$install_dir/backup.log"

    if [[ ! -f "$env_file" || ! -f "$compose_file" ]]; then
        ui::fail "no Synapse install at $install_dir (.env or docker-compose.yml missing)"
        return 2
    fi

    # Default output path: $INSTALL_DIR/backups/synapse-backup-<ts>.tar.gz
    local ts
    ts="$(date -u +%Y%m%d-%H%M%S 2>/dev/null || date +%s)"
    if [[ -z "$out_path" ]]; then
        local prefix=""
        prefix="$(detect::sudo_cmd 2>/dev/null || true)"
        $prefix mkdir -p "$install_dir/backups"
        out_path="$install_dir/backups/synapse-backup-${ts}.tar.gz"
    fi

    ui::step "Synapse backup"
    ui::info "Install dir: $install_dir"
    ui::info "Output: $out_path"

    local stage
    stage="$(mktemp -d 2>/dev/null || mktemp -d -t synapse-backup)"
    if [[ -n "$stage_var" ]]; then
        printf -v "$stage_var" '%s' "$stage"
    fi
    mkdir -p "$stage/volumes"

    # 1. Copy .env (unless --exclude-env) and docker-compose.yml.
    if (( ! exclude_env )); then
        cp "$env_file" "$stage/.env"
    fi
    cp "$compose_file" "$stage/docker-compose.yml"

    # 2. pg_dump the metadata DB. Read POSTGRES_USER/DB from .env so we
    #    don't hardcode "synapse"; operators may have customized.
    local pg_user pg_db
    pg_user="$(secrets::env_get "$env_file" POSTGRES_USER)"
    pg_db="$(secrets::env_get "$env_file" POSTGRES_DB)"
    pg_user="${pg_user:-synapse}"
    pg_db="${pg_db:-synapse}"

    local docker_cmd="${COMPOSE_CMD:-docker}"
    # `set -o pipefail` so the pipeline returns pg_dump's exit code,
    # not gzip's (gzip succeeds on an empty pipe and would mask a
    # postgres-down failure as a green backup).
    if ! ui::spin "Dumping metadata database ($pg_db)" \
            bash -c "set -o pipefail; '$docker_cmd' exec synapse-postgres pg_dump -U '$pg_user' -d '$pg_db' --clean --if-exists | gzip > '$stage/synapse.sql.gz'"; then
        ui::fail "pg_dump failed — is synapse-postgres running?"
        return 2
    fi

    # 3. Tar each per-deployment volume via a busybox sidecar. We use
    #    busybox so the host doesn't need tar; the volume is mounted
    #    read-only so live deployments can't corrupt the snapshot
    #    mid-stream.
    local volumes=()
    while IFS= read -r vol; do
        if [[ -n "$vol" ]]; then
            volumes+=("$vol")
        fi
    done < <("$docker_cmd" volume ls -q 2>/dev/null | grep -E '^synapse-data-' || true)

    local vol
    for vol in "${volumes[@]}"; do
        if ! ui::spin "Archiving volume $vol" \
                "$docker_cmd" run --rm \
                    -v "$vol:/source:ro" \
                    -v "$stage/volumes:/dest" \
                    "$LIFECYCLE_BACKUP_BUSYBOX_IMAGE" \
                    tar czf "/dest/$vol.tar.gz" -C /source .; then
            ui::warn "skipped $vol (tar failed)"
        fi
    done

    # 4. Manifest. Plain text key=value to keep it grep-able from the
    #    operator's terminal without needing jq. JSON would be nice
    #    but the dependencies aren't worth it for this footprint.
    {
        printf 'format=synapse-backup-v1\n'
        printf 'timestamp=%s\n' "$ts"
        printf 'version=%s\n' "$(secrets::env_get "$env_file" SYNAPSE_VERSION)"
        printf 'env_included=%s\n' "$(( exclude_env == 0 ))"
        printf 'volume_count=%s\n' "${#volumes[@]}"
        local v
        for v in "${volumes[@]}"; do
            printf 'volume=%s\n' "$v"
        done
    } > "$stage/manifest.txt"

    # 5. tar everything into the final archive.
    local prefix=""
    prefix="$(detect::sudo_cmd 2>/dev/null || true)"
    $prefix mkdir -p "$(dirname "$out_path")"
    if ! ui::spin "Bundling archive" \
            tar czf "$out_path" -C "$stage" .; then
        ui::fail "tar of staging dir failed"
        return 2
    fi

    local size
    size="$(du -h "$out_path" 2>/dev/null | awk '{print $1}')"
    lifecycle::log "$backup_log" "backup created: $out_path ($size)"

    # 6. Off-host: upload to S3 if --to-s3 was passed. Local tarball
    #    stays in place — operator can prune by hand. The s3 URI
    #    in the audit log is the canonical recovery target; the
    #    local copy is the safety net.
    if [[ -n "$to_s3" ]]; then
        # If --to-s3 ends with /, treat it as a directory and append
        # the basename of the local tarball. Otherwise use the URI
        # verbatim.
        local s3_target="$to_s3"
        if [[ "$s3_target" == */ ]]; then
            s3_target="${s3_target}$(basename "$out_path")"
        fi
        if ! ui::spin "Uploading to $s3_target" \
                s3::upload "$out_path" "$s3_target"; then
            ui::fail "S3 upload failed — local backup at $out_path is intact"
            lifecycle::log "$backup_log" "backup upload failed: $s3_target"
            return 2
        fi
        lifecycle::log "$backup_log" "backup uploaded: $s3_target"
        ui::success "Backup ready: $out_path → $s3_target ($size, ${#volumes[@]} volume(s))"
        return 0
    fi

    ui::success "Backup ready: $out_path ($size, ${#volumes[@]} volume(s))"
    return 0
}

# lifecycle::restore <install_dir> <archive_path> [--keep-env] [--non-interactive]
# Wipe per-deployment volumes + pgdata, restore from archive, bring
# stack back up. Requires the archive's manifest to validate. Returns
# 0 on success, 2 on failure.
lifecycle::restore() {
    local _stage_path=""
    local rc=0
    lifecycle::_restore_inner "$@" stage_var=_stage_path || rc=$?
    if [[ -n "$_stage_path" && -d "$_stage_path" ]]; then
        rm -rf "$_stage_path"
    fi
    return $rc
}

lifecycle::_restore_inner() {
    local install_dir="$1"
    local archive_path="$2"
    shift 2
    local keep_env=0 non_interactive=0 stage_var=""
    while (( $# > 0 )); do
        case "$1" in
            --keep-env)        keep_env=1 ;;
            --non-interactive) non_interactive=1 ;;
            stage_var=*)       stage_var="${1#*=}" ;;
        esac
        shift
    done

    local env_file="$install_dir/.env"
    local compose_file="$install_dir/docker-compose.yml"
    local restore_log="$install_dir/restore.log"

    if [[ ! -f "$env_file" || ! -f "$compose_file" ]]; then
        ui::fail "no Synapse install at $install_dir"
        return 2
    fi

    # If the operator passed an s3:// URI, download to a temp file
    # FIRST then point archive_path at it. The download lives in
    # /tmp and gets cleaned up via the same trap as the rest of the
    # restore staging.
    ui::step "Synapse restore"
    ui::info "Install dir: $install_dir"
    ui::info "Archive: $archive_path"

    local downloaded_archive=""
    if s3::is_s3_uri "$archive_path"; then
        if ! s3::check_creds 2>/dev/null; then
            ui::fail "s3:// archive requires AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY in env"
            ui::info "  for S3-compatible: also export SYNAPSE_BACKUP_S3_ENDPOINT"
            return 2
        fi
        downloaded_archive="$(mktemp 2>/dev/null || mktemp -t synapse)"
        # Rename to .tar.gz so any tooling that sniffs by extension
        # works (best-effort — failure here is non-fatal).
        mv "$downloaded_archive" "$downloaded_archive.tar.gz" 2>/dev/null \
            && downloaded_archive="$downloaded_archive.tar.gz"
        if ! ui::spin "Downloading from $archive_path" \
                s3::download "$archive_path" "$downloaded_archive"; then
            ui::fail "S3 download failed — check credentials, bucket, and key"
            rm -f "$downloaded_archive"
            return 2
        fi
        archive_path="$downloaded_archive"
    elif [[ ! -f "$archive_path" ]]; then
        ui::fail "archive not found: $archive_path"
        return 2
    fi

    # 1. Stage the archive contents.
    local stage
    stage="$(mktemp -d 2>/dev/null || mktemp -d -t synapse-restore)"
    if [[ -n "$stage_var" ]]; then
        printf -v "$stage_var" '%s' "$stage"
    fi

    if ! tar xzf "$archive_path" -C "$stage" 2>/dev/null; then
        ui::fail "archive could not be extracted (corrupt or wrong format?)"
        rm -f "$downloaded_archive"
        return 2
    fi
    # Clean up the downloaded tarball as soon as we've staged it; the
    # contents live in $stage now and the s3 copy is the canonical
    # backup. Skip when the archive is operator-supplied (local path).
    if [[ -n "$downloaded_archive" ]]; then
        rm -f "$downloaded_archive"
    fi
    if [[ ! -f "$stage/manifest.txt" ]]; then
        ui::fail "archive missing manifest.txt — not a Synapse backup"
        return 2
    fi

    # Validate format token.
    local format
    format="$(grep '^format=' "$stage/manifest.txt" | head -n1 | cut -d= -f2-)"
    if [[ "$format" != "synapse-backup-v1" ]]; then
        ui::fail "unsupported backup format: '$format' (expected synapse-backup-v1)"
        return 2
    fi

    local ts version vol_count
    ts="$(grep '^timestamp=' "$stage/manifest.txt" | head -n1 | cut -d= -f2-)"
    version="$(grep '^version=' "$stage/manifest.txt" | head -n1 | cut -d= -f2-)"
    vol_count="$(grep -c '^volume=' "$stage/manifest.txt" || true)"
    ui::info "Backup timestamp: ${ts:-unknown}"
    ui::info "Backup version: ${version:-unknown}"
    ui::info "Backup volumes: $vol_count"

    if (( ! non_interactive )); then
        printf 'This will WIPE current synapse-data-* volumes and pgdata. Continue? [y/N] ' >&2
        local reply=""
        read -r reply || true
        if [[ "$reply" != "y" && "$reply" != "Y" ]]; then
            ui::warn "aborted by operator"
            return 1
        fi
    fi

    lifecycle::log "$restore_log" "restore start: archive=$archive_path"

    local docker_cmd="${COMPOSE_CMD:-docker}"
    local pg_user pg_db
    pg_user="$(secrets::env_get "$env_file" POSTGRES_USER)"
    pg_db="$(secrets::env_get "$env_file" POSTGRES_DB)"
    pg_user="${pg_user:-synapse}"
    pg_db="${pg_db:-synapse}"

    # 2. Stop everything: compose stack AND synapse-managed deployment
    #    containers (which mount the synapse-data-* volumes — they'd
    #    block the volume rm otherwise).
    ui::spin "Stopping synapse-managed deployment containers" \
        bash -c "ids=\$('$docker_cmd' ps -aq --filter label=synapse.managed=true 2>/dev/null); if [[ -n \"\$ids\" ]]; then '$docker_cmd' rm -f \$ids >/dev/null 2>&1; fi; true"

    if ! ui::spin "Stopping compose stack" \
            "$docker_cmd" compose -f "$compose_file" down; then
        ui::fail "compose down failed"
        return 2
    fi

    # 3. Restore .env (unless --keep-env). The current .env stays put
    #    if the operator opted out — useful when the archive holds
    #    secrets they've since rotated.
    if (( ! keep_env )) && [[ -f "$stage/.env" ]]; then
        local prefix=""
        prefix="$(detect::sudo_cmd 2>/dev/null || true)"
        $prefix cp "$stage/.env" "$env_file"
        $prefix chmod 0600 "$env_file"
        ui::success ".env restored from archive"
    fi

    # 4. For each volume tarball in the archive, wipe + recreate the
    #    Docker volume + extract via busybox sidecar.
    if [[ -d "$stage/volumes" ]]; then
        local vol_archive vol
        for vol_archive in "$stage/volumes"/*.tar.gz; do
            [[ -e "$vol_archive" ]] || continue
            vol="$(basename "$vol_archive" .tar.gz)"
            "$docker_cmd" volume rm "$vol" >/dev/null 2>&1 || true
            "$docker_cmd" volume create "$vol" >/dev/null 2>&1 || true
            if ! ui::spin "Restoring volume $vol" \
                    "$docker_cmd" run --rm \
                        -v "$vol:/dest" \
                        -v "$stage/volumes:/src:ro" \
                        "$LIFECYCLE_BACKUP_BUSYBOX_IMAGE" \
                        tar xzf "/src/$(basename "$vol_archive")" -C /dest; then
                ui::warn "could not restore $vol"
            fi
        done
    fi

    # 5. Wipe pgdata so postgres comes up empty, ready to accept the
    #    pg_dump replay. Compose's project-name resolution is
    #    sensitive to COMPOSE_PROJECT_NAME, the compose file's
    #    parent dir, and operator overrides — we can't reliably
    #    predict the volume name. Match by suffix instead: any volume
    #    ending in `synapse-pgdata` is ours. (Real-VPS smoke caught a
    #    case where compose used 'synapse_synapse-pgdata' even though
    #    install_dir was /opt/synapse-test, leaving stale pgdata
    #    behind and breaking the password check on restore.)
    local pgdata_vol
    while IFS= read -r pgdata_vol; do
        [[ -n "$pgdata_vol" ]] || continue
        "$docker_cmd" volume rm "$pgdata_vol" >/dev/null 2>&1 || true
    done < <("$docker_cmd" volume ls -q 2>/dev/null | grep 'synapse-pgdata$' || true)

    # 6. Bring postgres up alone, wait for it, then pipe the dump in.
    if ! ui::spin "Starting postgres" \
            "$docker_cmd" compose -f "$compose_file" up -d postgres; then
        ui::fail "compose up postgres failed"
        return 2
    fi
    # Postgres on first-init runs an internal-then-real lifecycle:
    # boots, creates the user, SHUTS DOWN, restarts for real. Plain
    # `pg_isready` returns 0 during the internal boot, then connections
    # fail during the shutdown window with "the database system is
    # shutting down". We need a stronger gate: actually run a trivial
    # query and retry until it succeeds. 90s budget covers slow VPSes.
    local pg_ready=0
    local elapsed=0
    while (( elapsed < 90 )); do
        if "$docker_cmd" exec synapse-postgres \
                psql -U "$pg_user" -d "$pg_db" -tAc 'SELECT 1' >/dev/null 2>&1; then
            pg_ready=1
            break
        fi
        sleep 1
        elapsed=$(( elapsed + 1 ))
    done
    if (( ! pg_ready )); then
        ui::fail "postgres didn't accept queries in 90s"
        return 2
    fi

    if [[ -f "$stage/synapse.sql.gz" ]]; then
        # Decompress to a sibling .sql file so psql can read via a
        # plain `< file` redirect — avoids the bash -c + pipe combo
        # that swallowed psql's exit code on the synapse-test VPS
        # (the wrapper-rc was non-zero even when the dump itself
        # replayed cleanly when re-run by hand).
        local sql_file="$stage/synapse.sql"
        if ! gunzip -c "$stage/synapse.sql.gz" > "$sql_file"; then
            ui::fail "could not decompress synapse.sql.gz from archive"
            return 2
        fi
        ui::info "Replaying metadata dump"
        local replay_log="$stage/psql-replay.log"
        if ! "$docker_cmd" exec -i synapse-postgres psql \
                -U "$pg_user" -d "$pg_db" \
                -q -v ON_ERROR_STOP=1 < "$sql_file" >"$replay_log" 2>&1; then
            ui::fail "psql replay failed — see $replay_log"
            tail -n 20 "$replay_log" >&2 || true
            return 2
        fi
        ui::success "Replaying metadata dump"
    else
        ui::warn "no synapse.sql.gz in archive — skipping DB restore"
    fi

    # 7. Bring the rest of the stack up.
    if ! ui::spin "Starting full stack" \
            "$docker_cmd" compose -f "$compose_file" up -d; then
        ui::fail "compose up failed"
        return 2
    fi

    local synapse_port
    synapse_port="$(secrets::env_get "$env_file" SYNAPSE_PORT)"
    synapse_port="${synapse_port:-8080}"
    if ! compose::wait_healthy "http://localhost:$synapse_port/health" 120; then
        ui::fail "Synapse didn't become healthy in 120s after restore"
        return 2
    fi

    ui::success "Restore complete from $archive_path"
    lifecycle::log "$restore_log" "restore success: archive=$archive_path"
    return 0
}

# ====================================================================
# Uninstall (v0.6.1 chunk 3)
# ====================================================================
#
# Mandatory backup-first (--skip-backup overrides for unattended use).
# Then: stop synapse-managed deployment containers, compose down,
# wipe pgdata + synapse-data-* (default — see note), strip the
# host-Caddy managed block if it was a caddy_host install, rm
# install dir.
#
# Why purge volumes by default: pgdata is encrypted with .env's
# POSTGRES_PASSWORD; synapse-data-* admin keys are stored in
# postgres rows. Without the matching .env (which lives in the
# install dir we're about to nuke), the volumes are unusable —
# postgres will reject the new install's auth attempts (real-VPS
# smoke caught this on the v0.6.1 chunk 3 first run). The recovery
# path is: backup-first (always on) → re-install → --restore.
# `--keep-volumes` preserves them for advanced operators who saved
# the .env outside the install dir.

# lifecycle::uninstall <install_dir> [options]
# Options:
#   --skip-backup           Skip the mandatory pre-uninstall backup
#   --backup-out=<path>     Where to write the backup (default:
#                           /tmp/synapse-uninstall-backup-<ts>.tar.gz)
#   --keep-volumes          Preserve synapse-data-* + pgdata volumes
#                           (default is to wipe — see contract above)
#   --non-interactive       Skip the confirmation prompt
lifecycle::uninstall() {
    local install_dir="$1"
    shift
    local skip_backup=0 keep_volumes=0 non_interactive=0
    local backup_out=""
    while (( $# > 0 )); do
        case "$1" in
            --skip-backup)        skip_backup=1 ;;
            --backup-out=*)       backup_out="${1#*=}" ;;
            --backup-out)         backup_out="${2:-}"; shift ;;
            --keep-volumes)       keep_volumes=1 ;;
            --non-interactive)    non_interactive=1 ;;
        esac
        shift
    done

    local env_file="$install_dir/.env"
    local compose_file="$install_dir/docker-compose.yml"

    if [[ ! -f "$env_file" || ! -f "$compose_file" ]]; then
        ui::fail "no Synapse install at $install_dir"
        return 2
    fi

    ui::step "Synapse uninstall"
    ui::info "Install dir: $install_dir"
    if (( skip_backup )); then
        ui::warn "Skipping pre-uninstall backup (--skip-backup)"
    else
        if [[ -z "$backup_out" ]]; then
            local ts
            ts="$(date -u +%Y%m%d-%H%M%S 2>/dev/null || date +%s)"
            backup_out="/tmp/synapse-uninstall-backup-${ts}.tar.gz"
        fi
        ui::info "Pre-uninstall backup: $backup_out"
    fi
    if (( keep_volumes )); then
        ui::warn "Volumes preserved (--keep-volumes) — only useful if you saved .env"
    else
        ui::info "Volumes will be wiped (recovery via re-install + --restore=<backup>)"
    fi

    if (( ! non_interactive )); then
        printf 'This will REMOVE the Synapse install at %s. Continue? [y/N] ' "$install_dir" >&2
        local reply=""
        read -r reply || true
        if [[ "$reply" != "y" && "$reply" != "Y" ]]; then
            ui::warn "aborted by operator"
            return 1
        fi
    fi

    # 1. Pre-uninstall backup. We tolerate failure here (e.g. postgres
    #    already gone) to let the operator complete the uninstall —
    #    but we LOUDLY warn so they know they're nuking without a net.
    if (( ! skip_backup )); then
        if ! lifecycle::backup "$install_dir" --out="$backup_out"; then
            ui::warn "pre-uninstall backup FAILED — continuing anyway, but you have no rollback"
            ui::warn "abort and inspect with: docker compose -f $compose_file logs --tail=100"
            ui::warn "or re-run with --skip-backup if you intentionally want no backup"
        fi
    fi

    local docker_cmd="${COMPOSE_CMD:-docker}"

    # 2. Force-stop synapse-managed deployment containers. They mount
    #    synapse-data-* volumes; we need those locks released before
    #    docker volume rm (which is the default below).
    ui::spin "Stopping synapse-managed deployment containers" \
        bash -c "ids=\$('$docker_cmd' ps -aq --filter label=synapse.managed=true 2>/dev/null); if [[ -n \"\$ids\" ]]; then '$docker_cmd' rm -f \$ids >/dev/null 2>&1; fi; true"

    # 3. compose down. We deliberately do NOT pass --volumes here —
    #    compose only knows about the pgdata volume, not the
    #    synapse-managed ones, so we wipe everything by hand below
    #    (when not --keep-volumes). Same code path either way keeps
    #    the flow predictable.
    ui::spin "Stopping compose stack" \
        "$docker_cmd" compose -f "$compose_file" down

    # 4. Wipe synapse-data-* + pgdata (default; --keep-volumes opts out).
    if (( ! keep_volumes )); then
        local vol
        while IFS= read -r vol; do
            [[ -n "$vol" ]] || continue
            "$docker_cmd" volume rm "$vol" >/dev/null 2>&1 || true
        done < <("$docker_cmd" volume ls -q 2>/dev/null | grep -E '^synapse-data-' || true)

        # Match pgdata by suffix — compose's project-name resolution
        # depends on COMPOSE_PROJECT_NAME / compose-file's parent dir
        # / operator overrides, so we can't predict the exact name.
        # Anything ending in `synapse-pgdata` is ours.
        local pgdata_vol
        while IFS= read -r pgdata_vol; do
            [[ -n "$pgdata_vol" ]] || continue
            "$docker_cmd" volume rm "$pgdata_vol" >/dev/null 2>&1 || true
        done < <("$docker_cmd" volume ls -q 2>/dev/null | grep 'synapse-pgdata$' || true)
        ui::success "Volumes wiped"
    fi

    # 5. Strip the host-Caddy managed block if present. caddy_host
    #    mode is the only one that touches a shared file outside the
    #    install dir; the standalone Caddyfile lives in $install_dir
    #    and goes away with the rm -rf.
    local caddy_file="${SYNAPSE_HOST_CADDYFILE:-/etc/caddy/Caddyfile}"
    if [[ -f "$caddy_file" ]] && grep -q '# BEGIN synapse (managed by synapse setup.sh' "$caddy_file"; then
        local prefix=""
        prefix="$(detect::sudo_cmd 2>/dev/null || true)"
        $prefix bash -c "$(declare -f caddy::remove_block); caddy::remove_block '$caddy_file' synapse"
        ui::success "Removed managed block from $caddy_file"
        # Best-effort caddy reload — operator may have a non-systemd
        # caddy or it may already be down. Don't fail uninstall on
        # reload failure.
        if detect::has_cmd systemctl; then
            $prefix systemctl reload caddy 2>/dev/null \
                || $prefix systemctl restart caddy 2>/dev/null \
                || ui::warn "couldn't reload caddy — reload manually"
        fi
    fi

    # 6. Remove the install dir. Operator's pre-uninstall backup
    #    lives at $backup_out (outside $install_dir), so this is safe.
    local prefix=""
    prefix="$(detect::sudo_cmd 2>/dev/null || true)"
    if ! $prefix rm -rf "$install_dir"; then
        ui::fail "could not remove $install_dir — check permissions and inspect by hand"
        return 2
    fi
    ui::success "Removed $install_dir"

    if (( ! skip_backup )) && [[ -f "$backup_out" ]]; then
        ui::info ""
        ui::info "Backup preserved at: $backup_out"
        ui::info "To recover: re-install via setup.sh, then setup.sh --restore=$backup_out"
    fi
    ui::success "Synapse uninstalled."
    return 0
}

# ====================================================================
# Logs + Status (v0.6.1 chunk 4)
# ====================================================================
#
# `--logs <component>` is a thin pass-through to `docker compose logs`,
# scoped to the install dir's compose file so an operator with
# multiple Synapse instances doesn't have to cd around. Component
# names are validated against the known compose service set so a typo
# fails loudly instead of silently tailing nothing.
#
# `--status` is read-only — never mutates state. Designed to be the
# first thing an operator runs when something feels wrong: containers,
# volumes, public URL, DNS, TLS expiry, disk. Returns 0 when all green,
# 1 when something is degraded but recoverable, 2 when the install
# itself is broken (no .env / compose file).

# Components map 1:1 to compose service names. Listing them as an
# array keeps the validation message and the `case` arms in lock-step.
LIFECYCLE_LOG_COMPONENTS=(synapse dashboard postgres caddy convex-dashboard convex-dashboard-proxy)

# lifecycle::logs <install_dir> <component> [--follow] [--tail=<n>]
# Stream/dump compose logs for a single service. We deliberately do
# NOT capture to a file — operators almost always pipe to less / grep
# themselves, and saving a file would surprise --follow callers with
# never-finishing writes.
lifecycle::logs() {
    local install_dir="$1"
    local component="${2:-}"
    shift 2 2>/dev/null || true
    local follow=0 tail_n="200"
    while (( $# > 0 )); do
        case "$1" in
            --follow)   follow=1 ;;
            --tail=*)   tail_n="${1#*=}" ;;
            --tail)     tail_n="${2:-200}"; shift ;;
        esac
        shift
    done

    local compose_file="$install_dir/docker-compose.yml"
    if [[ ! -f "$compose_file" ]]; then
        ui::fail "no docker-compose.yml at $compose_file"
        return 2
    fi
    if [[ -z "$component" ]]; then
        ui::fail "missing component name"
        ui::info "Valid components: ${LIFECYCLE_LOG_COMPONENTS[*]}"
        return 2
    fi
    local known=0 c
    for c in "${LIFECYCLE_LOG_COMPONENTS[@]}"; do
        if [[ "$c" == "$component" ]]; then
            known=1
            break
        fi
    done
    if (( ! known )); then
        ui::fail "unknown component: $component"
        ui::info "Valid components: ${LIFECYCLE_LOG_COMPONENTS[*]}"
        return 2
    fi

    local docker_cmd="${COMPOSE_CMD:-docker}"
    local args=(compose -f "$compose_file" logs --tail="$tail_n")
    if (( follow )); then
        args+=(--follow)
    fi
    args+=("$component")
    "$docker_cmd" "${args[@]}"
}

# ---- status helpers ------------------------------------------------

# lifecycle::_status_row <label> <state> <message>
# Print a label + colored state + message in aligned columns. State is
# one of: ok | warn | fail. Used for the per-row health summary.
lifecycle::_status_row() {
    local label="$1" state="$2" message="$3"
    case "$state" in
        ok)   ui::success "$(printf '%-22s %s' "$label" "$message")" ;;
        warn) ui::warn    "$(printf '%-22s %s' "$label" "$message")" ;;
        fail) ui::fail    "$(printf '%-22s %s' "$label" "$message")" ;;
        *)    ui::info    "$(printf '%-22s %s' "$label" "$message")" ;;
    esac
}

# lifecycle::status <install_dir>
# Read-only diagnostic snapshot. Exit codes:
#   0 — all checks green
#   1 — at least one degraded check, install is recoverable
#   2 — install is broken (no .env / no compose file)
lifecycle::status() {
    local install_dir="$1"
    local env_file="$install_dir/.env"
    local compose_file="$install_dir/docker-compose.yml"

    if [[ ! -f "$env_file" ]]; then
        ui::fail "no .env at $env_file — is this a Synapse install dir?"
        return 2
    fi
    if [[ ! -f "$compose_file" ]]; then
        ui::fail "no docker-compose.yml at $compose_file"
        return 2
    fi

    ui::step "Synapse status"
    ui::info "Install dir: $install_dir"

    local degraded=0
    local docker_cmd="${COMPOSE_CMD:-docker}"
    local dig_cmd="${LIFECYCLE_DIG:-dig}"
    local openssl_cmd="${LIFECYCLE_OPENSSL:-openssl}"
    local df_cmd="${LIFECYCLE_DF:-df}"

    # ---- Version + Public URL + Custom domains (.env values) ------
    local version public_url base_domain
    version="$(secrets::env_get "$env_file" SYNAPSE_VERSION)"
    public_url="$(secrets::env_get "$env_file" SYNAPSE_PUBLIC_URL)"
    base_domain="$(secrets::env_get "$env_file" SYNAPSE_BASE_DOMAIN)"
    ui::info ""
    ui::info "$(printf '%-22s %s' "Version" "${version:-unknown}")"
    ui::info "$(printf '%-22s %s' "Public URL" "${public_url:-(unset — local-only)}")"
    if [[ -n "$base_domain" ]]; then
        ui::info "$(printf '%-22s %s' "Custom domains" "*.${base_domain} (v1.0)")"
    fi

    # ---- Compose stack containers ---------------------------------
    ui::info ""
    ui::info "Compose stack containers:"
    local ps_out
    if ps_out="$("$docker_cmd" compose -f "$compose_file" ps \
            --format '{{.Name}}\t{{.Image}}\t{{.State}}\t{{.Status}}' 2>/dev/null)" \
            && [[ -n "$ps_out" ]]; then
        while IFS=$'\t' read -r name image state status_str; do
            [[ -z "$name" ]] && continue
            local rstate="ok"
            case "$state" in
                running)  rstate="ok" ;;
                restarting|paused|created) rstate="warn"; degraded=1 ;;
                exited|dead) rstate="fail"; degraded=1 ;;
                *) rstate="warn"; degraded=1 ;;
            esac
            lifecycle::_status_row "  $name" "$rstate" "$image — $status_str"
        done <<< "$ps_out"
    else
        lifecycle::_status_row "Compose stack" "fail" "no containers (compose down?)"
        degraded=1
    fi

    # ---- Synapse-managed deployment containers --------------------
    ui::info ""
    local managed_count managed_names
    managed_names="$("$docker_cmd" ps --filter label=synapse.managed=true \
        --format '{{.Names}}' 2>/dev/null || true)"
    if [[ -z "$managed_names" ]]; then
        managed_count=0
    else
        managed_count="$(printf '%s\n' "$managed_names" | grep -c .)"
    fi
    lifecycle::_status_row "Deployment containers" "ok" "$managed_count running"
    if (( managed_count > 0 )); then
        local n
        while IFS= read -r n; do
            [[ -z "$n" ]] && continue
            ui::info "  - $n"
        done <<< "$managed_names"
    fi

    # ---- Volumes --------------------------------------------------
    ui::info ""
    ui::info "Volumes:"
    local vol_list
    vol_list="$("$docker_cmd" volume ls -q 2>/dev/null \
        | grep -E '^(synapse-data-|.*_synapse-pgdata$)' || true)"
    if [[ -z "$vol_list" ]]; then
        lifecycle::_status_row "  (none)" "warn" "no synapse-data-* / pgdata volumes found"
    else
        local v
        while IFS= read -r v; do
            [[ -z "$v" ]] && continue
            ui::info "  - $v"
        done <<< "$vol_list"
    fi

    # ---- DNS ------------------------------------------------------
    # Only meaningful when SYNAPSE_PUBLIC_URL is a hostname (not raw
    # IP, not localhost). Compare with the host's externally-visible
    # IP — when those don't match, the dashboard is reachable from
    # the local VPS but not from the operator's laptop.
    ui::info ""
    if [[ -n "$public_url" ]]; then
        local host
        host="${public_url#*://}"
        host="${host%%/*}"
        host="${host%%:*}"
        if [[ -n "$host" && ! "$host" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ \
                && "$host" != "localhost" ]]; then
            local resolved my_ip
            resolved="$("$dig_cmd" +short "$host" 2>/dev/null | head -n1)"
            my_ip="$(detect::public_ip 2>/dev/null || true)"
            if [[ -z "$resolved" ]]; then
                lifecycle::_status_row "DNS" "warn" "$host did not resolve"
                degraded=1
            elif [[ -n "$my_ip" && "$resolved" != "$my_ip" ]]; then
                lifecycle::_status_row "DNS" "warn" \
                    "$host -> $resolved (this VPS is $my_ip)"
                degraded=1
            else
                lifecycle::_status_row "DNS" "ok" "$host -> $resolved"
            fi
        else
            lifecycle::_status_row "DNS" "ok" "skipped (IP / localhost public URL)"
        fi

        # ---- TLS expiry --------------------------------------------
        if [[ "$public_url" == https://* && -n "$host" ]]; then
            local cert_end days_left
            cert_end="$(echo | "$openssl_cmd" s_client -servername "$host" \
                -connect "$host:443" 2>/dev/null \
                | "$openssl_cmd" x509 -noout -enddate 2>/dev/null \
                | sed 's/notAfter=//')"
            if [[ -n "$cert_end" ]]; then
                local end_epoch now_epoch
                end_epoch="$(date -d "$cert_end" +%s 2>/dev/null || echo 0)"
                now_epoch="$(date +%s)"
                if (( end_epoch > 0 )); then
                    days_left=$(( (end_epoch - now_epoch) / 86400 ))
                    if (( days_left < 0 )); then
                        lifecycle::_status_row "TLS cert" "fail" \
                            "expired ${days_left#-} days ago ($cert_end)"
                        degraded=1
                    elif (( days_left < 14 )); then
                        lifecycle::_status_row "TLS cert" "warn" \
                            "expires in $days_left days ($cert_end)"
                        degraded=1
                    else
                        lifecycle::_status_row "TLS cert" "ok" \
                            "expires in $days_left days ($cert_end)"
                    fi
                else
                    lifecycle::_status_row "TLS cert" "warn" \
                        "could not parse end date '$cert_end'"
                    degraded=1
                fi
            else
                lifecycle::_status_row "TLS cert" "warn" \
                    "could not fetch certificate (firewall? service down?)"
                degraded=1
            fi
        fi
    else
        lifecycle::_status_row "DNS" "ok" "skipped (no SYNAPSE_PUBLIC_URL)"
    fi

    # ---- Disk -----------------------------------------------------
    ui::info ""
    local df_target="/var/lib/docker"
    [[ -d "$df_target" ]] || df_target="/"
    local df_line
    df_line="$("$df_cmd" -hP "$df_target" 2>/dev/null | awk 'NR==2 {print}')"
    if [[ -n "$df_line" ]]; then
        lifecycle::_status_row "Disk ($df_target)" "ok" "$df_line"
    else
        lifecycle::_status_row "Disk ($df_target)" "warn" "df failed"
        degraded=1
    fi

    ui::info ""
    if (( degraded )); then
        ui::warn "Status: DEGRADED — see warnings above"
        return 1
    fi
    ui::success "Status: OK"
    return 0
}
