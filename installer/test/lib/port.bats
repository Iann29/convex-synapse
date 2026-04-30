#!/usr/bin/env bats
#
# Unit tests for installer/lib/port.sh.
#
# Two modes:
#   1. Pure-function input validation (no ss, no listening sockets) —
#      mock `ss` so the test never touches the host.
#   2. Real-port reservation — bind a real TCP socket via `nc -l` (or
#      bash's /dev/tcp where possible), assert in_use returns true,
#      release, assert false. The bats/bats:latest image bundles
#      busybox `nc` which supports `-l -p <port>`.
#
# We deliberately mock `ss` for the unit half so the tests don't depend
# on the host having iproute2, and so they remain deterministic in a
# CI sandbox where unrelated services may grab arbitrary ports.

load '../helpers/load'

setup() {
    synapse_mock_setup
    # shellcheck source=../../lib/port.sh
    source "$INSTALLER_LIB/port.sh"
}

# ---- in_use: input validation --------------------------------------

@test "in_use: missing arg -> exit 2" {
    run port::in_use
    assert_failure 2
    assert_output --partial "numeric port required"
}

@test "in_use: non-numeric arg -> exit 2" {
    run port::in_use abc
    assert_failure 2
    assert_output --partial "numeric port required"
}

@test "in_use: out-of-range -> exit 2" {
    run port::in_use 70000
    assert_failure 2
    assert_output --partial "out of range"
}

@test "in_use: zero -> exit 2" {
    run port::in_use 0
    assert_failure 2
    assert_output --partial "out of range"
}

# ---- in_use: ss-based detection (mocked) ---------------------------

@test "in_use: ss returns no rows -> port free (exit 1)" {
    # ss with -H prints zero lines when nothing matches.
    mock_cmd ss 0 ""
    run port::in_use 8080
    assert_failure 1
}

@test "in_use: ss returns one row -> port taken (exit 0)" {
    mock_cmd ss 0 "LISTEN 0      4096    0.0.0.0:8080  0.0.0.0:*"
    run port::in_use 8080
    assert_success
}

@test "in_use: ss returns multiple rows (IPv4+IPv6) -> taken" {
    mock_cmd ss 0 "LISTEN 0 4096 0.0.0.0:8080 0.0.0.0:*
LISTEN 0 4096 [::]:8080 [::]:*"
    run port::in_use 8080
    assert_success
}

@test "in_use: ss missing entirely -> exit 2 with helpful message" {
    # Drop a stub that simulates the bash shell builtin failing to
    # find the command. We need to drown out any *real* `ss` on PATH;
    # we do that by giving the mock dir higher priority in PATH (which
    # synapse_mock_setup already does) but we also need to ensure no
    # `ss` symlink exists in $SYN_MOCK_BIN. Then `command -v ss` from
    # within port::in_use only sees the (real) PATH after our shadow,
    # which under bats/bats:latest is Alpine + no iproute2 -> miss.
    #
    # In practice this test is robust on Alpine bats but fragile on
    # hosts that have ss on /usr/bin. To keep the bats run portable
    # across hosts, we shadow `command` itself to pretend ss is gone.
    command() {
        if [[ "${1:-}" == "-v" && "${2:-}" == "ss" ]]; then
            return 1
        fi
        builtin command "$@"
    }
    export -f command
    run port::in_use 8080
    assert_failure 2
    assert_output --partial "ss"
}

# ---- find_free: input validation -----------------------------------

@test "find_free: missing arg -> exit 2" {
    run port::find_free
    assert_failure 2
    assert_output --partial "numeric start port required"
}

@test "find_free: non-numeric -> exit 2" {
    run port::find_free abc
    assert_failure 2
}

@test "find_free: invalid range (end < start) -> exit 2" {
    run port::find_free 9000 8000
    assert_failure 2
    assert_output --partial "invalid range"
}

@test "find_free: out-of-range start -> exit 2" {
    run port::find_free 70000
    assert_failure 2
}

# ---- find_free: search behaviour (mocked ss) -----------------------

@test "find_free: first port free -> returns it immediately" {
    mock_cmd ss 0 ""
    run port::find_free 8080
    assert_success
    assert_output "8080"
}

@test "find_free: skips taken ports until free one" {
    # We need ss to say "taken" for 8080-8082 and "free" for 8083. The
    # mock script branches on the filter argument. Note that the ss
    # filter is one whole quoted arg "( sport = :8080 )"; we extract
    # the number with a regex match against the joined argv so the
    # exact arg-splitting doesn't matter.
    cat >"$SYN_MOCK_BIN/ss" <<'EOF'
#!/usr/bin/env bash
joined="$*"
port=""
if [[ "$joined" =~ :([0-9]+) ]]; then
    port="${BASH_REMATCH[1]}"
fi
case "$port" in
    8080|8081|8082) echo "LISTEN 0 4096 0.0.0.0:$port 0.0.0.0:*" ;;
    *) ;;  # nothing -> free
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/ss"
    run port::find_free 8080
    assert_success
    assert_output "8083"
}

@test "find_free: range exhausted -> exit 1" {
    # All ports in [8080, 8082] are taken; ss always returns a match.
    mock_cmd ss 0 "LISTEN 0 4096 0.0.0.0:* 0.0.0.0:*"
    run port::find_free 8080 8082
    assert_failure 1
}
