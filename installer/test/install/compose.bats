#!/usr/bin/env bats
#
# Unit tests for installer/install/compose.sh.
#
# Mocks `docker` and `curl` via PATH-shadow. The wait_healthy timeout
# is shrunk via COMPOSE_HEALTH_TIMEOUT_OVERRIDE so the suite stays
# fast.

bats_require_minimum_version 1.5.0

load '../helpers/load'

setup() {
    synapse_mock_setup
    # shellcheck source=../../install/ui.sh
    source "$INSTALLER_DIR/install/ui.sh"
    # shellcheck source=../../install/compose.sh
    source "$INSTALLER_DIR/install/compose.sh"
    UI_NO_COLOR=1
}

# ---- pull / up / down ----------------------------------------------

@test "pull: invokes docker compose pull on the supplied dir" {
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
echo "$@" >>"$BATS_TEST_TMPDIR/docker.calls"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" run compose::pull "/opt/synapse"
    assert_success
    run cat "$BATS_TEST_TMPDIR/docker.calls"
    assert_output --partial "compose -f /opt/synapse/docker-compose.yml pull"
}

@test "up: passes --profile flags through" {
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
echo "$@" >>"$BATS_TEST_TMPDIR/docker.calls"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" run compose::up "/opt/synapse" --profile caddy
    assert_success
    run cat "$BATS_TEST_TMPDIR/docker.calls"
    assert_output --partial "--profile caddy"
    assert_output --partial "up -d"
}

@test "up: multiple --profile flags accumulate" {
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
echo "$@" >>"$BATS_TEST_TMPDIR/docker.calls"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" run compose::up "." --profile caddy --profile ha
    assert_success
    run cat "$BATS_TEST_TMPDIR/docker.calls"
    assert_output --partial "--profile caddy"
    assert_output --partial "--profile ha"
}

@test "down: emits compose down without volumes by default" {
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
echo "$@" >>"$BATS_TEST_TMPDIR/docker.calls"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" run compose::down "/opt/synapse"
    assert_success
    run cat "$BATS_TEST_TMPDIR/docker.calls"
    assert_output --partial "down"
    refute_output --partial "--volumes"
}

@test "down --volumes: passes the destructive flag" {
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
echo "$@" >>"$BATS_TEST_TMPDIR/docker.calls"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker" run compose::down "." --volumes
    assert_success
    run cat "$BATS_TEST_TMPDIR/docker.calls"
    assert_output --partial "--volumes"
}

# ---- wait_healthy --------------------------------------------------

@test "wait_healthy: missing url -> exit 2" {
    run compose::wait_healthy
    assert_failure 2
    assert_output --partial "url required"
}

@test "wait_healthy: curl 0 -> success on first try" {
    mock_cmd curl 0
    COMPOSE_CURL="$SYN_MOCK_BIN/curl" \
    COMPOSE_HEALTH_TIMEOUT_OVERRIDE=5 \
        run compose::wait_healthy "http://localhost:8080/health"
    assert_success
}

@test "wait_healthy: curl always non-zero -> times out" {
    mock_cmd curl 7
    COMPOSE_CURL="$SYN_MOCK_BIN/curl" \
    COMPOSE_HEALTH_TIMEOUT_OVERRIDE=2 \
        run compose::wait_healthy "http://localhost:8080/health"
    assert_failure 1
}

@test "wait_healthy: succeeds on Nth attempt (mock flips after 2 calls)" {
    cat >"$SYN_MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
counter_file="$BATS_TEST_TMPDIR/curl.counter"
n=0
[[ -f "$counter_file" ]] && n=$(cat "$counter_file")
n=$((n + 1))
echo "$n" >"$counter_file"
if (( n < 3 )); then exit 7; fi
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/curl"
    COMPOSE_CURL="$SYN_MOCK_BIN/curl" \
    COMPOSE_HEALTH_TIMEOUT_OVERRIDE=10 \
        run compose::wait_healthy "http://localhost:8080/health"
    assert_success
    run cat "$BATS_TEST_TMPDIR/curl.counter"
    [[ "$output" -ge 3 ]]
}
