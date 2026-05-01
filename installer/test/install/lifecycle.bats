#!/usr/bin/env bats
#
# Unit tests for installer/install/lifecycle.sh.
#
# Mocks every external command via PATH-shadow: curl (GitHub Releases
# API), jq (JSON extraction), git (clone), docker (compose images,
# tag, pull). The wait_healthy timeout is shrunk via
# COMPOSE_HEALTH_TIMEOUT_OVERRIDE so health-failure tests don't hang.

bats_require_minimum_version 1.5.0

load '../helpers/load'

setup() {
    synapse_mock_setup
    # shellcheck source=../../install/ui.sh
    source "$INSTALLER_DIR/install/ui.sh"
    # shellcheck source=../../install/secrets.sh
    source "$INSTALLER_DIR/install/secrets.sh"
    # shellcheck source=../../install/compose.sh
    source "$INSTALLER_DIR/install/compose.sh"
    # detect.sh is needed for sudo_cmd / has_cmd inside lifecycle::upgrade.
    # shellcheck source=../../lib/detect.sh
    source "$INSTALLER_DIR/lib/detect.sh"
    # shellcheck source=../../install/lifecycle.sh
    source "$INSTALLER_DIR/install/lifecycle.sh"
    UI_NO_COLOR=1
    INSTALL_DIR="$BATS_TEST_TMPDIR/install"
    mkdir -p "$INSTALL_DIR"
    ENV_FILE="$INSTALL_DIR/.env"
    COMPOSE_FILE="$INSTALL_DIR/docker-compose.yml"
}

# ---- resolve_target_ref ---------------------------------------------

@test "resolve_target_ref: explicit override is returned verbatim" {
    run lifecycle::resolve_target_ref "v0.6.5"
    assert_success
    assert_output "v0.6.5"
}

@test "resolve_target_ref: GitHub releases tag is used when API succeeds" {
    cat >"$SYN_MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
echo '{"tag_name":"v0.7.0","name":"v0.7.0"}'
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/curl"
    # We're testing resolve_target_ref's "use whatever jq returns"
    # behavior, not jq itself. Simplest mock: ignore stdin, print tag.
    cat >"$SYN_MOCK_BIN/jq" <<'EOF'
#!/usr/bin/env bash
cat >/dev/null
echo "v0.7.0"
EOF
    chmod +x "$SYN_MOCK_BIN/jq"
    LIFECYCLE_CURL="$SYN_MOCK_BIN/curl" LIFECYCLE_JQ="$SYN_MOCK_BIN/jq" \
        run lifecycle::resolve_target_ref ""
    assert_success
    assert_output "v0.7.0"
}

@test "resolve_target_ref: falls back to main when API fails" {
    cat >"$SYN_MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
exit 22
EOF
    chmod +x "$SYN_MOCK_BIN/curl"
    LIFECYCLE_CURL="$SYN_MOCK_BIN/curl" \
        run lifecycle::resolve_target_ref ""
    assert_success
    assert_output "main"
}

@test "resolve_target_ref: falls back to main when API returns empty tag" {
    cat >"$SYN_MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
echo '{"message":"Not Found"}'
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/curl"
    cat >"$SYN_MOCK_BIN/jq" <<'EOF'
#!/usr/bin/env bash
# .tag_name is missing, return empty
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/jq"
    LIFECYCLE_CURL="$SYN_MOCK_BIN/curl" LIFECYCLE_JQ="$SYN_MOCK_BIN/jq" \
        run lifecycle::resolve_target_ref ""
    assert_success
    assert_output "main"
}

# ---- current_version ------------------------------------------------

@test "current_version: returns SYNAPSE_VERSION from .env" {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=0.6.0
EOF
    run lifecycle::current_version "$ENV_FILE"
    assert_success
    assert_output "0.6.0"
}

@test "current_version: empty when stamp missing (older installs)" {
    cat >"$ENV_FILE" <<EOF
POSTGRES_PASSWORD=foo
EOF
    run lifecycle::current_version "$ENV_FILE"
    assert_success
    assert_output ""
}

# ---- snapshot_images ------------------------------------------------

@test "snapshot_images: writes service<TAB>repo:tag<TAB>id rows" {
    : >"$COMPOSE_FILE"  # presence is enough
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == "compose" && "$5" == "images" ]]; then
    cat <<JSON
[
  {"ContainerName":"synapse","Repository":"convex2-synapse","Tag":"latest","ID":"sha256:aaa"},
  {"ContainerName":"postgres","Repository":"postgres","Tag":"16","ID":"sha256:bbb"}
]
JSON
fi
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    cat >"$SYN_MOCK_BIN/jq" <<'EOF'
#!/usr/bin/env bash
# Trivial fake: emit two TSV rows that match the docker mock above.
printf 'synapse\tconvex2-synapse:latest\tsha256:aaa\n'
printf 'postgres\tpostgres:16\tsha256:bbb\n'
EOF
    chmod +x "$SYN_MOCK_BIN/jq"
    local snap="$INSTALL_DIR/.upgrade-snapshot.tsv"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" LIFECYCLE_JQ="$SYN_MOCK_BIN/jq" \
        run lifecycle::snapshot_images "$INSTALL_DIR" "$snap"
    assert_success
    [ -f "$snap" ]
    run cat "$snap"
    assert_output --partial $'synapse\tconvex2-synapse:latest\tsha256:aaa'
    assert_output --partial $'postgres\tpostgres:16\tsha256:bbb'
}

# ---- rollback_images ------------------------------------------------

@test "rollback_images: re-tags every image_id and brings stack up" {
    local snap="$INSTALL_DIR/.upgrade-snapshot.tsv"
    printf 'synapse\tconvex2-synapse:latest\tsha256:aaa\n'  >"$snap"
    printf 'postgres\tpostgres:16\tsha256:bbb\n'            >>"$snap"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
echo "$@" >>"$BATS_TEST_TMPDIR/docker.calls"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    : >"$COMPOSE_FILE"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        run lifecycle::rollback_images "$snap" "$INSTALL_DIR"
    run cat "$BATS_TEST_TMPDIR/docker.calls"
    assert_output --partial "tag sha256:aaa convex2-synapse:latest"
    assert_output --partial "tag sha256:bbb postgres:16"
    assert_output --partial "compose -f $INSTALL_DIR/docker-compose.yml up -d"
}

@test "rollback_images: returns 1 when snapshot is empty/missing" {
    run lifecycle::rollback_images "$BATS_TEST_TMPDIR/nope.tsv" "$INSTALL_DIR"
    assert_failure
}

# ---- detect_profiles ------------------------------------------------

@test "detect_profiles: emits caddy when standalone Caddyfile exists" {
    : >"$ENV_FILE"
    : >"$INSTALL_DIR/Caddyfile"
    run lifecycle::detect_profiles "$ENV_FILE"
    assert_success
    assert_output --partial "--profile"
    assert_output --partial "caddy"
}

@test "detect_profiles: emits ha when SYNAPSE_HA_ENABLED=true" {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_HA_ENABLED=true
EOF
    run lifecycle::detect_profiles "$ENV_FILE"
    assert_success
    assert_output --partial "--profile"
    assert_output --partial "ha"
}

@test "detect_profiles: emits nothing for a vanilla single-replica install" {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_HA_ENABLED=false
EOF
    run lifecycle::detect_profiles "$ENV_FILE"
    assert_success
    assert_output ""
}

# ---- upgrade: validation --------------------------------------------

@test "upgrade: aborts with clear message when .env is missing" {
    : >"$COMPOSE_FILE"
    run lifecycle::upgrade "$INSTALL_DIR"
    assert_failure 2
    assert_output --partial "no .env"
}

@test "upgrade: aborts when docker-compose.yml is missing" {
    : >"$ENV_FILE"
    run lifecycle::upgrade "$INSTALL_DIR"
    assert_failure 2
    assert_output --partial "no docker-compose.yml"
}

# ---- upgrade: short-circuit / force ---------------------------------

@test "upgrade: short-circuits when current == target (no --force)" {
    : >"$COMPOSE_FILE"
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=0.6.1
EOF
    run lifecycle::upgrade "$INSTALL_DIR" --ref=v0.6.1
    assert_success
    assert_output --partial "Already on 0.6.1"
}

@test "upgrade: NEVER short-circuits when target is a feat/* branch" {
    : >"$COMPOSE_FILE"
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=feat/installer-upgrade
EOF
    cat >"$SYN_MOCK_BIN/git" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
    chmod +x "$SYN_MOCK_BIN/git"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    cat >"$SYN_MOCK_BIN/jq" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/jq"
    LIFECYCLE_GIT="$SYN_MOCK_BIN/git" COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        LIFECYCLE_JQ="$SYN_MOCK_BIN/jq" \
        run lifecycle::upgrade "$INSTALL_DIR" --ref=feat/installer-upgrade
    assert_failure 2
    refute_output --partial "Already on"
    assert_output --partial "git clone failed"
}

@test "upgrade: NEVER short-circuits when target is main (moving target)" {
    # Rig `git clone` to fail so we fast-fail at step 5 — proves we
    # got past the short-circuit. We only care that we don't bail at
    # step 3 with "already on main".
    : >"$COMPOSE_FILE"
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=main
EOF
    cat >"$SYN_MOCK_BIN/git" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
    chmod +x "$SYN_MOCK_BIN/git"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    cat >"$SYN_MOCK_BIN/jq" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/jq"
    LIFECYCLE_GIT="$SYN_MOCK_BIN/git" COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        LIFECYCLE_JQ="$SYN_MOCK_BIN/jq" \
        run lifecycle::upgrade "$INSTALL_DIR" --ref=main
    assert_failure 2
    refute_output --partial "Already on"
    assert_output --partial "git clone failed"
}

@test "upgrade: --force bypasses the short-circuit" {
    : >"$COMPOSE_FILE"
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=0.6.1
EOF
    cat >"$SYN_MOCK_BIN/git" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
    chmod +x "$SYN_MOCK_BIN/git"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    cat >"$SYN_MOCK_BIN/jq" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/jq"
    LIFECYCLE_GIT="$SYN_MOCK_BIN/git" COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        LIFECYCLE_JQ="$SYN_MOCK_BIN/jq" \
        run lifecycle::upgrade "$INSTALL_DIR" --ref=v0.6.1 --force
    assert_failure 2
    refute_output --partial "Already on"
}

# ---- upgrade: rollback on health failure ----------------------------

@test "upgrade: rolls back when /health never goes 2xx" {
    : >"$COMPOSE_FILE"
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=0.6.0
SYNAPSE_PORT=8080
EOF
    # Override snapshot_images to a no-op AFTER we pre-write the file
    # below — the real one truncates and re-fills via jq, which our
    # silent jq mock would leave empty, defeating the rollback path.
    eval 'lifecycle::snapshot_images() { :; }'
    # Pre-existing snapshot so rollback has something to do.
    printf 'synapse\tlocal/synapse:latest\tsha256:old\n' >"$INSTALL_DIR/.upgrade-snapshot.tsv"
    cat >"$SYN_MOCK_BIN/git" <<'EOF'
#!/usr/bin/env bash
# Last arg is the clone destination (git clone ... <url> <dest>). Use
# ${@: -1} to grab it portably regardless of how many flags precede.
dest="${@: -1}"
mkdir -p "$dest"
echo "fake" >"$dest/README.md"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/git"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
echo "$@" >>"$BATS_TEST_TMPDIR/docker.calls"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    cat >"$SYN_MOCK_BIN/jq" <<'EOF'
#!/usr/bin/env bash
# Quietly succeed; snapshot output is via earlier docker call which
# we ignore here because we already pre-wrote the snapshot file.
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/jq"
    # Curl always 500s — health never passes.
    cat >"$SYN_MOCK_BIN/curl_fail" <<'EOF'
#!/usr/bin/env bash
exit 22
EOF
    chmod +x "$SYN_MOCK_BIN/curl_fail"
    LIFECYCLE_GIT="$SYN_MOCK_BIN/git" \
        COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        LIFECYCLE_JQ="$SYN_MOCK_BIN/jq" \
        COMPOSE_CURL="$SYN_MOCK_BIN/curl_fail" \
        COMPOSE_HEALTH_TIMEOUT_OVERRIDE=2 \
        run lifecycle::upgrade "$INSTALL_DIR" --ref=v0.6.1
    assert_failure 2
    assert_output --partial "didn't become healthy"
    assert_output --partial "Rolling back"
    run cat "$BATS_TEST_TMPDIR/docker.calls"
    # Rollback re-tags the snapshot's image_id.
    assert_output --partial "tag sha256:old local/synapse:latest"
    # Audit log was written.
    [ -f "$INSTALL_DIR/upgrade.log" ]
    run cat "$INSTALL_DIR/upgrade.log"
    assert_output --partial "upgrade failed: health"
    assert_output --partial "rollback:"
}

# ---- upgrade: happy path stamps version + preserves .env -----------

@test "upgrade: happy path stamps SYNAPSE_VERSION and preserves .env body" {
    : >"$COMPOSE_FILE"
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=0.6.0
SYNAPSE_PORT=8080
SYNAPSE_JWT_SECRET=must-be-preserved-jwt
POSTGRES_PASSWORD=must-be-preserved-pg
EOF
    cat >"$SYN_MOCK_BIN/git" <<'EOF'
#!/usr/bin/env bash
# Last arg is the clone destination. The clone does NOT carry .env —
# proves rsync excludes it.
dest="${@: -1}"
mkdir -p "$dest"
echo "fake" >"$dest/README.md"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/git"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    cat >"$SYN_MOCK_BIN/jq" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/jq"
    cat >"$SYN_MOCK_BIN/curl_ok" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/curl_ok"
    LIFECYCLE_GIT="$SYN_MOCK_BIN/git" \
        COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        LIFECYCLE_JQ="$SYN_MOCK_BIN/jq" \
        COMPOSE_CURL="$SYN_MOCK_BIN/curl_ok" \
        COMPOSE_HEALTH_TIMEOUT_OVERRIDE=2 \
        run lifecycle::upgrade "$INSTALL_DIR" --ref=v0.6.1
    assert_success
    assert_output --partial "Upgrade complete"
    run secrets::env_get "$ENV_FILE" SYNAPSE_VERSION
    assert_output "0.6.1"
    run secrets::env_get "$ENV_FILE" SYNAPSE_JWT_SECRET
    assert_output "must-be-preserved-jwt"
    run secrets::env_get "$ENV_FILE" POSTGRES_PASSWORD
    assert_output "must-be-preserved-pg"
    [ -f "$INSTALL_DIR/upgrade.log" ]
    run cat "$INSTALL_DIR/upgrade.log"
    assert_output --partial "upgrade success: 0.6.0 → 0.6.1"
    # README.md from the fake clone made it through rsync.
    [ -f "$INSTALL_DIR/README.md" ]
}

@test "upgrade: sanitizes slashes in branch refs before stamping" {
    : >"$COMPOSE_FILE"
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=0.6.0
SYNAPSE_PORT=8080
EOF
    cat >"$SYN_MOCK_BIN/git" <<'EOF'
#!/usr/bin/env bash
dest="${@: -1}"
mkdir -p "$dest"
echo "fake" >"$dest/README.md"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/git"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    cat >"$SYN_MOCK_BIN/jq" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/jq"
    cat >"$SYN_MOCK_BIN/curl_ok" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/curl_ok"
    LIFECYCLE_GIT="$SYN_MOCK_BIN/git" \
        COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        LIFECYCLE_JQ="$SYN_MOCK_BIN/jq" \
        COMPOSE_CURL="$SYN_MOCK_BIN/curl_ok" \
        COMPOSE_HEALTH_TIMEOUT_OVERRIDE=2 \
        run lifecycle::upgrade "$INSTALL_DIR" --ref=feat/installer-upgrade
    assert_success
    run secrets::env_get "$ENV_FILE" SYNAPSE_VERSION
    # Slashes replaced with hyphens — docker image tags reject '/'.
    assert_output "feat-installer-upgrade"
}

# ====================================================================
# backup / restore (v0.6.1 chunk 2)
# ====================================================================

# Helper: minimal install fixture for backup tests.
_setup_install_fixture() {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=0.6.1
POSTGRES_USER=synapse
POSTGRES_DB=synapse
SYNAPSE_PORT=8080
EOF
    cat >"$COMPOSE_FILE" <<EOF
services:
  synapse: {}
EOF
}

# ---- backup: validation -------------------------------------------

@test "backup: aborts when .env is missing" {
    : >"$COMPOSE_FILE"
    run lifecycle::backup "$INSTALL_DIR"
    assert_failure 2
    assert_output --partial "no Synapse install"
}

@test "backup: aborts when docker-compose.yml is missing" {
    : >"$ENV_FILE"
    run lifecycle::backup "$INSTALL_DIR"
    assert_failure 2
    assert_output --partial "no Synapse install"
}

# ---- backup: happy path -------------------------------------------

@test "backup: produces a tarball with manifest + .env + db dump" {
    _setup_install_fixture
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    exec)
        # docker exec synapse-postgres pg_dump ...
        if [[ "$2" == "synapse-postgres" && "$3" == "pg_dump" ]]; then
            echo "-- fake pg_dump output --"
            exit 0
        fi
        ;;
    volume)
        case "$2" in
            ls) printf 'synapse-data-foo\nsynapse-data-bar\nother-vol\n' ;;
        esac
        ;;
    run)
        # docker run --rm -v $vol:/source ... busybox tar czf /dest/$vol.tar.gz
        # Find the output tar path from the args.
        out=""
        seen=0
        for a in "$@"; do
            if (( seen == 1 )); then out="$a"; break; fi
            [[ "$a" == "tar" ]] && seen=1
            true
        done
        # Cheap synthesis: write an empty gzip-compatible file at the
        # mounted dest path. The mount target is $stage/volumes (host
        # side); we infer it from the -v args. Simpler: create a 1-byte
        # file at the path the next arg names, but that's complex.
        # Easiest: just make the tar binary work. We only need the
        # presence of /volumes/<vol>.tar.gz in the staged dir; busybox
        # is mocked out, so we have to re-create that effect.
        : # no-op, the staged volumes dir stays empty for unit tests
        ;;
esac
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    local out_path="$BATS_TEST_TMPDIR/test-backup.tar.gz"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        run lifecycle::backup "$INSTALL_DIR" --out="$out_path"
    assert_success
    assert_output --partial "Backup ready"
    [ -f "$out_path" ]

    # Inspect the archive
    local extract_dir="$BATS_TEST_TMPDIR/extract"
    mkdir -p "$extract_dir"
    tar xzf "$out_path" -C "$extract_dir"
    [ -f "$extract_dir/manifest.txt" ]
    [ -f "$extract_dir/.env" ]
    [ -f "$extract_dir/docker-compose.yml" ]
    [ -f "$extract_dir/synapse.sql.gz" ]
    run grep '^format=' "$extract_dir/manifest.txt"
    assert_output "format=synapse-backup-v1"
    run grep '^env_included=' "$extract_dir/manifest.txt"
    assert_output "env_included=1"
}

@test "backup: --exclude-env omits .env from archive" {
    _setup_install_fixture
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    exec)   [[ "$3" == "pg_dump" ]] && echo "-- dump --" ;;
    volume) [[ "$2" == "ls" ]] && true ;;
    run)    : ;;
esac
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    local out_path="$BATS_TEST_TMPDIR/no-env.tar.gz"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        run lifecycle::backup "$INSTALL_DIR" --out="$out_path" --exclude-env
    assert_success
    local extract_dir="$BATS_TEST_TMPDIR/no-env-extract"
    mkdir -p "$extract_dir"
    tar xzf "$out_path" -C "$extract_dir"
    [ ! -f "$extract_dir/.env" ]
    run grep '^env_included=' "$extract_dir/manifest.txt"
    assert_output "env_included=0"
}

@test "backup: aborts when pg_dump fails (postgres not running)" {
    _setup_install_fixture
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
[[ "$1" == "exec" ]] && exit 1   # pg_dump fails
[[ "$1" == "volume" ]] && true
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        run lifecycle::backup "$INSTALL_DIR" --out="$BATS_TEST_TMPDIR/wont-exist.tar.gz"
    assert_failure 2
    assert_output --partial "pg_dump failed"
}

# ---- restore: validation ------------------------------------------

@test "restore: aborts when archive is missing" {
    _setup_install_fixture
    run lifecycle::restore "$INSTALL_DIR" "/nope.tar.gz" --non-interactive
    assert_failure 2
    assert_output --partial "archive not found"
}

@test "restore: aborts when archive lacks manifest.txt" {
    _setup_install_fixture
    local bad_archive="$BATS_TEST_TMPDIR/bad.tar.gz"
    local stage="$BATS_TEST_TMPDIR/bad-stage"
    mkdir -p "$stage"
    echo "junk" >"$stage/random.txt"
    tar czf "$bad_archive" -C "$stage" .
    run lifecycle::restore "$INSTALL_DIR" "$bad_archive" --non-interactive
    assert_failure 2
    assert_output --partial "missing manifest.txt"
}

@test "restore: aborts on unknown manifest format" {
    _setup_install_fixture
    local bad_archive="$BATS_TEST_TMPDIR/wrong-fmt.tar.gz"
    local stage="$BATS_TEST_TMPDIR/wrong-fmt-stage"
    mkdir -p "$stage"
    echo "format=synapse-backup-v999" >"$stage/manifest.txt"
    tar czf "$bad_archive" -C "$stage" .
    run lifecycle::restore "$INSTALL_DIR" "$bad_archive" --non-interactive
    assert_failure 2
    assert_output --partial "unsupported backup format"
}

@test "restore: --keep-env preserves the current .env across restore" {
    _setup_install_fixture
    # Pre-existing .env we want preserved
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=0.6.1
POSTGRES_USER=synapse
POSTGRES_DB=synapse
SYNAPSE_PORT=8080
SYNAPSE_JWT_SECRET=must-not-be-clobbered
EOF
    # Build an archive whose .env is DIFFERENT
    local archive="$BATS_TEST_TMPDIR/with-env.tar.gz"
    local stage="$BATS_TEST_TMPDIR/with-env-stage"
    mkdir -p "$stage/volumes"
    cat >"$stage/manifest.txt" <<EOF
format=synapse-backup-v1
timestamp=20260501-120000
version=0.6.1
env_included=1
volume_count=0
EOF
    cat >"$stage/.env" <<EOF
SYNAPSE_JWT_SECRET=archive-secret-different
POSTGRES_USER=synapse
POSTGRES_DB=synapse
EOF
    : >"$stage/docker-compose.yml"
    # A real (if empty-content) gzip blob — `: > file` produces 0
    # bytes, which gunzip rejects with "invalid magic".
    printf 'SELECT 1;\n' | gzip >"$stage/synapse.sql.gz"
    tar czf "$archive" -C "$stage" .

    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
# minimal: succeed for compose down/up, exec pg_isready, exec psql
case "$1" in
    ps)     echo "" ;;
    rm)     true ;;
    compose|exec|volume|run) true ;;
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    cat >"$SYN_MOCK_BIN/curl_ok" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/curl_ok"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        COMPOSE_CURL="$SYN_MOCK_BIN/curl_ok" \
        COMPOSE_HEALTH_TIMEOUT_OVERRIDE=2 \
        run lifecycle::restore "$INSTALL_DIR" "$archive" --keep-env --non-interactive
    assert_success
    run secrets::env_get "$ENV_FILE" SYNAPSE_JWT_SECRET
    # Original preserved, archive's value rejected.
    assert_output "must-not-be-clobbered"
}

@test "restore: replaces .env from archive by default" {
    _setup_install_fixture
    cat >"$ENV_FILE" <<EOF
SYNAPSE_JWT_SECRET=before-restore
POSTGRES_USER=synapse
POSTGRES_DB=synapse
SYNAPSE_PORT=8080
EOF
    local archive="$BATS_TEST_TMPDIR/replaces.tar.gz"
    local stage="$BATS_TEST_TMPDIR/replaces-stage"
    mkdir -p "$stage/volumes"
    cat >"$stage/manifest.txt" <<EOF
format=synapse-backup-v1
timestamp=20260501-120000
version=0.6.1
env_included=1
volume_count=0
EOF
    cat >"$stage/.env" <<EOF
SYNAPSE_JWT_SECRET=after-restore
POSTGRES_USER=synapse
POSTGRES_DB=synapse
EOF
    : >"$stage/docker-compose.yml"
    # A real (if empty-content) gzip blob — `: > file` produces 0
    # bytes, which gunzip rejects with "invalid magic".
    printf 'SELECT 1;\n' | gzip >"$stage/synapse.sql.gz"
    tar czf "$archive" -C "$stage" .

    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    ps) echo "" ;;
    *)  true ;;
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    cat >"$SYN_MOCK_BIN/curl_ok" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/curl_ok"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        COMPOSE_CURL="$SYN_MOCK_BIN/curl_ok" \
        COMPOSE_HEALTH_TIMEOUT_OVERRIDE=2 \
        run lifecycle::restore "$INSTALL_DIR" "$archive" --non-interactive
    assert_success
    run secrets::env_get "$ENV_FILE" SYNAPSE_JWT_SECRET
    assert_output "after-restore"
}

# ====================================================================
# uninstall (v0.6.1 chunk 3)
# ====================================================================

@test "uninstall: aborts when .env is missing" {
    : >"$COMPOSE_FILE"
    run lifecycle::uninstall "$INSTALL_DIR" --non-interactive
    assert_failure 2
    assert_output --partial "no Synapse install"
}

@test "uninstall: --non-interactive + --skip-backup nukes the install dir" {
    _setup_install_fixture
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    ps) echo "" ;;
    *)  true ;;
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        run lifecycle::uninstall "$INSTALL_DIR" --non-interactive --skip-backup
    assert_success
    assert_output --partial "Synapse uninstalled"
    [ ! -d "$INSTALL_DIR" ]
}

@test "uninstall: takes a backup at /tmp by default before removing" {
    _setup_install_fixture
    # Stub `docker` for compose down + rm + volume + exec (pg_dump)
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    exec)   [[ "$3" == "pg_dump" ]] && echo "-- dump --" ;;
    volume) [[ "$2" == "ls" ]] && true ;;
    run)    : ;;
    ps)     echo "" ;;
    rm|compose|tag) true ;;
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    local backup_out="$BATS_TEST_TMPDIR/un-backup.tar.gz"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        run lifecycle::uninstall "$INSTALL_DIR" \
            --non-interactive \
            --backup-out="$backup_out"
    assert_success
    [ -f "$backup_out" ]
    [ ! -d "$INSTALL_DIR" ]
}

@test "uninstall: wipes synapse-data-* + pgdata by default" {
    _setup_install_fixture
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
echo "$@" >>"$BATS_TEST_TMPDIR/docker.calls"
case "$1" in
    volume)
        case "$2" in
            ls)
                printf 'synapse-data-foo\nsynapse-data-bar\nsomething-else\n'
                # Two pgdata candidates a real install might have
                # depending on compose project-name resolution.
                printf 'install_synapse-pgdata\nsynapse_synapse-pgdata\n'
                ;;
        esac
        ;;
    ps) echo "" ;;
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        run lifecycle::uninstall "$INSTALL_DIR" \
            --non-interactive --skip-backup
    assert_success
    run cat "$BATS_TEST_TMPDIR/docker.calls"
    assert_output --partial "volume rm synapse-data-foo"
    assert_output --partial "volume rm synapse-data-bar"
    # Suffix-match wipes EVERY volume ending in synapse-pgdata —
    # avoids the predict-the-project-name trap.
    assert_output --partial "volume rm install_synapse-pgdata"
    assert_output --partial "volume rm synapse_synapse-pgdata"
    refute_output --partial "volume rm something-else"
}

@test "uninstall: --keep-volumes preserves volumes (only useful with saved .env)" {
    _setup_install_fixture
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
echo "$@" >>"$BATS_TEST_TMPDIR/docker.calls"
case "$1" in
    volume)
        case "$2" in
            ls) printf 'synapse-data-foo\n' ;;
        esac
        ;;
    ps) echo "" ;;
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        run lifecycle::uninstall "$INSTALL_DIR" \
            --non-interactive --skip-backup --keep-volumes
    assert_success
    run cat "$BATS_TEST_TMPDIR/docker.calls"
    refute_output --partial "volume rm synapse-data-foo"
    refute_output --partial "volume rm install_synapse-pgdata"
}

@test "uninstall: aborts when operator declines confirmation" {
    _setup_install_fixture
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    # No --non-interactive: lifecycle::uninstall reads stdin for the
    # confirm prompt. Feed "n" via here-string so it bails before
    # touching anything.
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        run lifecycle::uninstall "$INSTALL_DIR" --skip-backup <<<"n"
    assert_failure
    assert_output --partial "aborted by operator"
    [ -d "$INSTALL_DIR" ]
}

# ====================================================================
# logs (v0.6.1 chunk 4)
# ====================================================================

@test "logs: aborts when component name is missing" {
    : >"$COMPOSE_FILE"
    run lifecycle::logs "$INSTALL_DIR" ""
    assert_failure 2
    assert_output --partial "missing component"
    assert_output --partial "synapse"
    assert_output --partial "convex-dashboard-proxy"
}

@test "logs: aborts when component name is unknown and lists valid ones" {
    : >"$COMPOSE_FILE"
    run lifecycle::logs "$INSTALL_DIR" "nope"
    assert_failure 2
    assert_output --partial "unknown component: nope"
    assert_output --partial "synapse"
    assert_output --partial "dashboard"
    assert_output --partial "postgres"
    assert_output --partial "caddy"
    assert_output --partial "convex-dashboard"
}

@test "logs: aborts when docker-compose.yml is missing" {
    run lifecycle::logs "$INSTALL_DIR" "synapse"
    assert_failure 2
    assert_output --partial "no docker-compose.yml"
}

@test "logs: defaults to compose logs --tail=200 for valid component" {
    : >"$COMPOSE_FILE"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
echo "$@" >>"$BATS_TEST_TMPDIR/docker.calls"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        run lifecycle::logs "$INSTALL_DIR" "synapse"
    assert_success
    run cat "$BATS_TEST_TMPDIR/docker.calls"
    assert_output --partial "compose -f $INSTALL_DIR/docker-compose.yml logs --tail=200 synapse"
    refute_output --partial "--follow"
}

@test "logs: passes --follow when --follow flag is set" {
    : >"$COMPOSE_FILE"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
echo "$@" >>"$BATS_TEST_TMPDIR/docker.calls"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        run lifecycle::logs "$INSTALL_DIR" "dashboard" --follow
    assert_success
    run cat "$BATS_TEST_TMPDIR/docker.calls"
    assert_output --partial "logs --tail=200 --follow dashboard"
}

@test "logs: forwards a custom --tail=<n>" {
    : >"$COMPOSE_FILE"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
echo "$@" >>"$BATS_TEST_TMPDIR/docker.calls"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        run lifecycle::logs "$INSTALL_DIR" "postgres" --tail=42
    assert_success
    run cat "$BATS_TEST_TMPDIR/docker.calls"
    assert_output --partial "logs --tail=42 postgres"
}

@test "logs: convex-dashboard-proxy is a valid component" {
    : >"$COMPOSE_FILE"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
echo "$@" >>"$BATS_TEST_TMPDIR/docker.calls"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        run lifecycle::logs "$INSTALL_DIR" "convex-dashboard-proxy"
    assert_success
    run cat "$BATS_TEST_TMPDIR/docker.calls"
    assert_output --partial "convex-dashboard-proxy"
}

# ====================================================================
# status (v0.6.1 chunk 4)
# ====================================================================

@test "status: aborts when .env is missing" {
    : >"$COMPOSE_FILE"
    run lifecycle::status "$INSTALL_DIR"
    assert_failure 2
    assert_output --partial "no .env"
}

@test "status: aborts when docker-compose.yml is missing" {
    : >"$ENV_FILE"
    run lifecycle::status "$INSTALL_DIR"
    assert_failure 2
    assert_output --partial "no docker-compose.yml"
}

@test "status: prints version + public URL from .env on a healthy stack" {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=0.6.1
SYNAPSE_PUBLIC_URL=https://synapse.example.com
EOF
    : >"$COMPOSE_FILE"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    compose)
        # `compose -f <file> ps --format ...`
        printf 'synapse-postgres\tpostgres:16\trunning\tUp 2 hours\n'
        printf 'synapse\tlocal/synapse:latest\trunning\tUp 1 hour\n'
        ;;
    ps)
        # docker ps --filter label=synapse.managed=true
        printf 'happy-deployment\nfast-deployment\n'
        ;;
    volume)
        printf 'synapse-data-foo\nsynapse-data-bar\ninstall_synapse-pgdata\nother\n'
        ;;
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    cat >"$SYN_MOCK_BIN/dig" <<'EOF'
#!/usr/bin/env bash
echo "1.2.3.4"
EOF
    chmod +x "$SYN_MOCK_BIN/dig"
    cat >"$SYN_MOCK_BIN/openssl" <<'EOF'
#!/usr/bin/env bash
# First call: s_client → emit cert text on stdout, ignored.
# Second call: x509 -noout -enddate → emit a far-future expiry.
# We use ISO-8601 instead of OpenSSL's `Dec 31 23:59:59 2099 GMT`
# because busybox `date -d` (Alpine in the bats image) only parses
# ISO-8601. Real systems use GNU date which handles both.
if [[ "$1" == "s_client" ]]; then
    echo "(fake-cert)"
    exit 0
fi
if [[ "$1" == "x509" ]]; then
    echo "notAfter=2099-12-31 23:59:59"
    exit 0
fi
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/openssl"
    cat >"$SYN_MOCK_BIN/df" <<'EOF'
#!/usr/bin/env bash
echo "Filesystem      Size  Used Avail Use% Mounted on"
echo "/dev/sda1       100G   20G   80G  20% /"
EOF
    chmod +x "$SYN_MOCK_BIN/df"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        LIFECYCLE_DIG="$SYN_MOCK_BIN/dig" \
        LIFECYCLE_OPENSSL="$SYN_MOCK_BIN/openssl" \
        LIFECYCLE_DF="$SYN_MOCK_BIN/df" \
        DETECT_PUBLIC_IP_OVERRIDE="1.2.3.4" \
        run lifecycle::status "$INSTALL_DIR"
    assert_success
    assert_output --partial "Version"
    assert_output --partial "0.6.1"
    assert_output --partial "synapse.example.com"
    assert_output --partial "synapse-postgres"
    assert_output --partial "Deployment containers"
    assert_output --partial "2 running"
    assert_output --partial "synapse-data-foo"
    assert_output --partial "DNS"
    assert_output --partial "TLS cert"
    assert_output --partial "Status: OK"
}

@test "status: counts deployment containers via docker ps --filter label" {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=0.6.1
EOF
    : >"$COMPOSE_FILE"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
echo "$@" >>"$BATS_TEST_TMPDIR/docker.calls"
case "$1" in
    compose) printf 'synapse\tlocal/synapse:latest\trunning\tUp\n' ;;
    ps)
        # Sanity: confirm the filter we received.
        printf 'd-one\nd-two\nd-three\n'
        ;;
    volume) : ;;
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    cat >"$SYN_MOCK_BIN/df" <<'EOF'
#!/usr/bin/env bash
echo "Filesystem Size Used Avail Use% Mounted on"
echo "/dev/x 1G 1G 0G 0% /"
EOF
    chmod +x "$SYN_MOCK_BIN/df"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        LIFECYCLE_DF="$SYN_MOCK_BIN/df" \
        run lifecycle::status "$INSTALL_DIR"
    # 3 deployment containers — degraded=0 still, returns 0.
    assert_success
    assert_output --partial "3 running"
    run cat "$BATS_TEST_TMPDIR/docker.calls"
    assert_output --partial "ps --filter label=synapse.managed=true"
}

@test "status: returns 1 (degraded) when a compose service is exited" {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=0.6.1
EOF
    : >"$COMPOSE_FILE"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    compose)
        printf 'synapse-postgres\tpostgres:16\trunning\tUp\n'
        printf 'synapse\tlocal/synapse:latest\texited\tExited (1) 2 minutes ago\n'
        ;;
    ps)     echo "" ;;
    volume) : ;;
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    cat >"$SYN_MOCK_BIN/df" <<'EOF'
#!/usr/bin/env bash
echo "Filesystem Size Used Avail Use% Mounted on"
echo "/dev/x 1G 1G 0G 0% /"
EOF
    chmod +x "$SYN_MOCK_BIN/df"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        LIFECYCLE_DF="$SYN_MOCK_BIN/df" \
        run lifecycle::status "$INSTALL_DIR"
    assert_failure 1
    # `Exited` is in the docker ps Status field; case-sensitive match
    # so we don't false-positive on other words.
    assert_output --partial "Exited"
    assert_output --partial "DEGRADED"
}

@test "status: returns 1 (degraded) when DNS doesn't match this VPS" {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=0.6.1
SYNAPSE_PUBLIC_URL=https://wrong.example.com
EOF
    : >"$COMPOSE_FILE"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    compose) printf 'synapse\tlocal/synapse:latest\trunning\tUp\n' ;;
    ps)      echo "" ;;
    volume)  : ;;
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    cat >"$SYN_MOCK_BIN/dig" <<'EOF'
#!/usr/bin/env bash
echo "9.9.9.9"
EOF
    chmod +x "$SYN_MOCK_BIN/dig"
    cat >"$SYN_MOCK_BIN/openssl" <<'EOF'
#!/usr/bin/env bash
# Pretend cert fetch silently fails (firewall) so we don't double-warn.
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/openssl"
    cat >"$SYN_MOCK_BIN/df" <<'EOF'
#!/usr/bin/env bash
echo "Filesystem Size Used Avail Use% Mounted on"
echo "/dev/x 1G 1G 0G 0% /"
EOF
    chmod +x "$SYN_MOCK_BIN/df"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        LIFECYCLE_DIG="$SYN_MOCK_BIN/dig" \
        LIFECYCLE_OPENSSL="$SYN_MOCK_BIN/openssl" \
        LIFECYCLE_DF="$SYN_MOCK_BIN/df" \
        DETECT_PUBLIC_IP_OVERRIDE="1.2.3.4" \
        run lifecycle::status "$INSTALL_DIR"
    assert_failure 1
    assert_output --partial "wrong.example.com -> 9.9.9.9"
    assert_output --partial "DEGRADED"
}

@test "status: shows custom-domains row when SYNAPSE_BASE_DOMAIN is set" {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=1.0.0
SYNAPSE_PUBLIC_URL=https://synapse.example.com
SYNAPSE_BASE_DOMAIN=synapse.example.com
EOF
    : >"$COMPOSE_FILE"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    compose) printf 'synapse-api\tlocal/synapse:latest\trunning\tUp 1 minute\n' ;;
    ps) ;;
    volume) ;;
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    cat >"$SYN_MOCK_BIN/dig" <<'EOF'
#!/usr/bin/env bash
echo "1.2.3.4"
EOF
    chmod +x "$SYN_MOCK_BIN/dig"
    cat >"$SYN_MOCK_BIN/openssl" <<'EOF'
#!/usr/bin/env bash
[[ "$1" == "s_client" ]] && { echo "(fake)"; exit 0; }
[[ "$1" == "x509" ]] && { echo "notAfter=2099-12-31 23:59:59"; exit 0; }
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/openssl"
    cat >"$SYN_MOCK_BIN/df" <<'EOF'
#!/usr/bin/env bash
echo "Filesystem Size Used Avail Use% Mounted on"
echo "/dev/x 100G 20G 80G 20% /"
EOF
    chmod +x "$SYN_MOCK_BIN/df"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        LIFECYCLE_DIG="$SYN_MOCK_BIN/dig" \
        LIFECYCLE_OPENSSL="$SYN_MOCK_BIN/openssl" \
        LIFECYCLE_DF="$SYN_MOCK_BIN/df" \
        DETECT_PUBLIC_IP_OVERRIDE="1.2.3.4" \
        run lifecycle::status "$INSTALL_DIR"
    assert_success
    assert_output --partial "Custom domains"
    assert_output --partial "*.synapse.example.com"
}

@test "status: omits custom-domains row when SYNAPSE_BASE_DOMAIN is unset" {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=0.6.3
SYNAPSE_PUBLIC_URL=https://synapse.example.com
EOF
    : >"$COMPOSE_FILE"
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    compose) printf 'synapse-api\tlocal/synapse:latest\trunning\tUp 1 minute\n' ;;
    ps) ;;
    volume) ;;
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    cat >"$SYN_MOCK_BIN/dig" <<'EOF'
#!/usr/bin/env bash
echo "1.2.3.4"
EOF
    chmod +x "$SYN_MOCK_BIN/dig"
    cat >"$SYN_MOCK_BIN/openssl" <<'EOF'
#!/usr/bin/env bash
[[ "$1" == "s_client" ]] && { echo "(fake)"; exit 0; }
[[ "$1" == "x509" ]] && { echo "notAfter=2099-12-31 23:59:59"; exit 0; }
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/openssl"
    cat >"$SYN_MOCK_BIN/df" <<'EOF'
#!/usr/bin/env bash
echo "Filesystem Size Used Avail Use% Mounted on"
echo "/dev/x 100G 20G 80G 20% /"
EOF
    chmod +x "$SYN_MOCK_BIN/df"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" \
        LIFECYCLE_DIG="$SYN_MOCK_BIN/dig" \
        LIFECYCLE_OPENSSL="$SYN_MOCK_BIN/openssl" \
        LIFECYCLE_DF="$SYN_MOCK_BIN/df" \
        DETECT_PUBLIC_IP_OVERRIDE="1.2.3.4" \
        run lifecycle::status "$INSTALL_DIR"
    assert_success
    refute_output --partial "Custom domains"
}
