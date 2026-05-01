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
