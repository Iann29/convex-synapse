#!/usr/bin/env bats
#
# End-to-end smoke for the synapse-updater Python daemon. Boots a real
# `python3 installer/updater/synapse-updater` against a loopback TCP
# port + a tmpdir-scoped state directory, then exercises the
# bearer-token gate via real `curl` requests.
#
# This is a black-box test — we only touch the daemon through its HTTP
# surface. It catches regressions that the existing source-grep tests
# don't (e.g. someone moves the auth check into _handle_upgrade only,
# leaving /healthz unauthenticated).
#
# Skipped when python3 is missing (the bats container without python3
# is still useful for the bash-only suites).

bats_require_minimum_version 1.5.0

load '../helpers/load'

REPO_ROOT="$(cd "$BATS_TEST_DIRNAME/../../.." && pwd)"
DAEMON_SRC="$REPO_ROOT/installer/updater/synapse-updater"
# Pick a high port unlikely to clash with an operator's prod daemon
# (which defaults to 8089) or any common dev service.
TEST_PORT=18089
TEST_TOKEN="bats-token-$$-$RANDOM"

setup() {
    if ! command -v python3 >/dev/null 2>&1; then
        skip "python3 not available in this bats container"
    fi
    if ! command -v curl >/dev/null 2>&1; then
        skip "curl not available in this bats container"
    fi

    # Per-test state — the daemon writes status.json + lock files there.
    # Pointing both STATE_DIR and LOG_DIR at $BATS_TEST_TMPDIR keeps the
    # test hermetic; the real defaults (/var/lib/, /var/log/) would
    # require root + persist between bats runs.
    DAEMON_STATE="$BATS_TEST_TMPDIR/state"
    DAEMON_LOG="$BATS_TEST_TMPDIR/log"
    mkdir -p "$DAEMON_STATE" "$DAEMON_LOG"
    DAEMON_PID=""
    DAEMON_STDERR="$BATS_TEST_TMPDIR/daemon.stderr"
}

teardown() {
    if [[ -n "${DAEMON_PID:-}" ]] && kill -0 "$DAEMON_PID" 2>/dev/null; then
        kill "$DAEMON_PID" 2>/dev/null || true
        # Give it a beat to exit cleanly so the next test doesn't
        # collide on the same port.
        for _ in 1 2 3 4 5; do
            kill -0 "$DAEMON_PID" 2>/dev/null || break
            sleep 0.1
        done
        kill -9 "$DAEMON_PID" 2>/dev/null || true
    fi
}

# Launch the daemon in the background and wait for it to bind. Returns
# nonzero (via fail) if the bind never happens — that's a real bug, not
# a flaky test, and we want it loud.
start_daemon() {
    local token="${1:-$TEST_TOKEN}"
    SYNAPSE_UPDATER_TOKEN="$token" \
    SYNAPSE_UPDATER_PORT="$TEST_PORT" \
    SYNAPSE_UPDATER_BIND="127.0.0.1" \
    SYNAPSE_UPDATER_STATE_DIR="$DAEMON_STATE" \
    SYNAPSE_UPDATER_LOG_DIR="$DAEMON_LOG" \
    SYNAPSE_INSTALL_DIR="$BATS_TEST_TMPDIR/install" \
        python3 "$DAEMON_SRC" 2>"$DAEMON_STDERR" &
    DAEMON_PID=$!

    # Poll up to ~3s for the listener to come up. /healthz returns 401
    # without the token but a 401 IS proof the daemon bound the port.
    local i
    for i in $(seq 1 30); do
        if curl -s -o /dev/null --max-time 1 "http://127.0.0.1:$TEST_PORT/healthz"; then
            return 0
        fi
        sleep 0.1
    done
    echo "daemon did not bind 127.0.0.1:$TEST_PORT after 3s" >&2
    cat "$DAEMON_STDERR" >&2 || true
    return 1
}

# ---- happy path -----------------------------------------------------

@test "daemon: /healthz returns 200 with valid bearer token" {
    start_daemon
    run curl -sS -o /dev/null -w "%{http_code}" \
        -H "Authorization: Bearer $TEST_TOKEN" \
        "http://127.0.0.1:$TEST_PORT/healthz"
    assert_success
    assert_output "200"
}

@test "daemon: /healthz body is {\"ok\":true} with valid token" {
    start_daemon
    run curl -sS -H "Authorization: Bearer $TEST_TOKEN" \
        "http://127.0.0.1:$TEST_PORT/healthz"
    assert_success
    assert_output --partial '"ok"'
    assert_output --partial 'true'
}

# ---- unauthenticated paths ------------------------------------------
#
# /healthz is intentionally token-gated (clean threat model: only the
# api server with the shared secret should be able to probe the daemon
# at all). Any future PR that "helpfully" exempts /healthz to make the
# liveness check easier should fail this test.

@test "daemon: /healthz returns 401 without Authorization header" {
    start_daemon
    run curl -sS -o /dev/null -w "%{http_code}" \
        "http://127.0.0.1:$TEST_PORT/healthz"
    assert_success
    assert_output "401"
}

@test "daemon: /healthz returns 401 with wrong bearer token" {
    start_daemon
    run curl -sS -o /dev/null -w "%{http_code}" \
        -H "Authorization: Bearer wrong-token" \
        "http://127.0.0.1:$TEST_PORT/healthz"
    assert_success
    assert_output "401"
}

@test "daemon: 401 response includes WWW-Authenticate: Bearer" {
    start_daemon
    run curl -sS -o /dev/null -D - \
        "http://127.0.0.1:$TEST_PORT/healthz"
    assert_success
    assert_output --partial "WWW-Authenticate: Bearer"
}

@test "daemon: /status is also token-gated" {
    start_daemon
    run curl -sS -o /dev/null -w "%{http_code}" \
        "http://127.0.0.1:$TEST_PORT/status"
    assert_success
    assert_output "401"
}

@test "daemon: POST /upgrade is token-gated (401 even with valid JSON body)" {
    start_daemon
    run curl -sS -o /dev/null -w "%{http_code}" \
        -X POST -H "Content-Type: application/json" \
        -d '{"ref":"main"}' \
        "http://127.0.0.1:$TEST_PORT/upgrade"
    assert_success
    assert_output "401"
}

# ---- token-empty refusal --------------------------------------------
#
# Empty SYNAPSE_UPDATER_TOKEN must abort before the listener binds.
# An unauthenticated loopback HTTP server that can run setup.sh as root
# is a security hole — the daemon refuses outright rather than running
# in a permissive mode the operator might miss.

@test "daemon: refuses to start when SYNAPSE_UPDATER_TOKEN is empty" {
    # Important: do NOT pass a token. Don't use start_daemon (which
    # waits for a successful bind).
    SYNAPSE_UPDATER_PORT="$TEST_PORT" \
    SYNAPSE_UPDATER_BIND="127.0.0.1" \
    SYNAPSE_UPDATER_STATE_DIR="$DAEMON_STATE" \
    SYNAPSE_UPDATER_LOG_DIR="$DAEMON_LOG" \
    SYNAPSE_INSTALL_DIR="$BATS_TEST_TMPDIR/install" \
        run python3 "$DAEMON_SRC"
    # Nonzero exit + a warning on stderr is the contract; assert both
    # so a future PR that swaps "exit 1" for "exit 0 + log warning"
    # still fails this test.
    assert_failure
    assert_output --partial "SYNAPSE_UPDATER_TOKEN is empty"
}

# ---- listener address sanity ----------------------------------------
#
# A regression where someone defaults BIND_ADDR to 0.0.0.0 (or
# accidentally drops the host arg entirely) would expose the daemon to
# the public internet. We can't directly observe the listener's bind
# address without parsing /proc/net/tcp, but we can verify by trying a
# request to a non-loopback hostname and getting a connection error
# rather than a 401.

@test "daemon: stderr line announces TCP localhost listener" {
    start_daemon
    # Give the OS one more tick to flush the announcement.
    sleep 0.1
    run cat "$DAEMON_STDERR"
    assert_success
    assert_output --partial "listening on http://127.0.0.1:$TEST_PORT"
}
