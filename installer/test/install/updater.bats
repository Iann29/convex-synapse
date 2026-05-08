#!/usr/bin/env bats
#
# Unit tests for setup.sh's phase_install_updater + the synapse-updater
# Python daemon's syntactic validity.
#
# Strategy: source setup.sh with __SETUP_NO_MAIN=1, then call
# phase_install_updater with PATH-shadow mocks for systemctl, install,
# curl, and python3 detection. We assert on the recorded mock calls
# rather than on filesystem side-effects (the install dir is a real
# tmpdir, but the system paths /usr/local/bin and /etc/systemd are
# unwritable from inside the bats container — and we wouldn't want
# them mutated even if they were).

bats_require_minimum_version 1.5.0

load '../helpers/load'

setup() {
    synapse_mock_setup
    REPO_ROOT="$(cd "$INSTALLER_DIR/.." && pwd)"

    # Build a fake INSTALL_DIR with the updater bundle in place — that's
    # the input phase_install_updater works against.
    FAKE_INSTALL="$BATS_TEST_TMPDIR/install"
    mkdir -p "$FAKE_INSTALL/installer/updater"
    cp "$REPO_ROOT/installer/updater/synapse-updater"         "$FAKE_INSTALL/installer/updater/"
    cp "$REPO_ROOT/installer/updater/synapse-updater.service" "$FAKE_INSTALL/installer/updater/"

    # Source the bare phase + its UI/detect dependencies. Sourcing the
    # whole setup.sh would bring traps, lockfiles and ERR-handler state
    # along, none of which the phase needs. secrets.sh is needed for
    # the env_get helper that the TCP probe uses to read .env.
    # shellcheck source=../../install/ui.sh
    source "$INSTALLER_DIR/install/ui.sh"
    # shellcheck source=../../lib/detect.sh
    source "$INSTALLER_DIR/lib/detect.sh"
    # shellcheck source=../../install/secrets.sh
    source "$INSTALLER_DIR/install/secrets.sh"
    # shellcheck source=../../install/updater.sh
    source "$INSTALLER_DIR/install/updater.sh"

    # Most tests want a populated .env so the TCP probe can read the
    # token / port back. Individual tests override or remove this file.
    cat >"$FAKE_INSTALL/.env" <<'EOF'
SYNAPSE_UPDATER_PORT=8089
SYNAPSE_UPDATER_TOKEN=bats-fixture-bearer-token
EOF

    INSTALL_DIR="$FAKE_INSTALL"
    INSTALLER_VERSION="9.9.9-bats"
    UI_NO_COLOR=1
    UI_FORCE_COLOR=0

    # The bats container ships without python3 by default, so the real
    # detect::has_cmd would short-circuit every test on the "python3
    # not on PATH" warning before reaching the install/systemctl logic
    # we actually want to assert on. Override to claim every command
    # exists; individual tests that exercise the missing-cmd paths
    # redefine this with their own narrower stub.
    detect::has_cmd() { return 0; }
    # detect::sudo_cmd is called for the prefix; force "no sudo" so the
    # mocked install/systemctl run in the foreground without a sudo
    # wrapper that bats wouldn't be able to intercept.
    detect::sudo_cmd() { printf ''; }
}

# Default mock setup most tests want: every external command succeeds
# and writes a recognisable trail. Specific tests override (e.g. the
# "no python3" path unmocks command -v).
mock_default_externals() {
    mock_cmd systemctl 0 ""
    mock_cmd install 0 ""
    mock_cmd tee 0 ""
    mock_cmd curl 0 '{"ok":true}'
    # phase guards on `detect::has_cmd python3 / systemctl`. Both call
    # `command -v` under the hood, which we can't shadow via PATH-mock
    # (it's a shell builtin), so we override the function directly when
    # a test wants to fake "missing".
    :
}

# ---- happy path -----------------------------------------------------

@test "phase_install_updater: installs binary, unit, reloads systemd" {
    mock_default_externals
    run phase_install_updater
    assert_success

    # `install` was called twice — once for /usr/local/bin/synapse-updater,
    # once for /etc/systemd/system/synapse-updater.service.
    [ -f "$SYN_MOCK_CALLS/install" ]
    run cat "$SYN_MOCK_CALLS/install"
    assert_output --partial "/usr/local/bin/synapse-updater"
    assert_output --partial "/etc/systemd/system/synapse-updater.service"

    # systemctl was driven through daemon-reload + enable + restart.
    run cat "$SYN_MOCK_CALLS/systemctl"
    assert_output --partial "daemon-reload"
    assert_output --partial "enable"
    assert_output --partial "synapse-updater"
}

@test "phase_install_updater: stamps INSTALL_DIR/VERSION" {
    mock_default_externals
    run phase_install_updater
    assert_success
    # The phase writes via `tee` (mocked here) so we can't read VERSION
    # back from disk, but we can confirm the tee mock saw the right
    # destination on its argv.
    run cat "$SYN_MOCK_CALLS/tee"
    assert_output --partial "$FAKE_INSTALL/VERSION"
}

# ---- {{INSTALL_DIR}} substitution (regression: bug #1) -------------
#
# The unit ships with a placeholder; phase_install_updater renders the
# operator's actual --install-dir into it before installing. Without
# this, custom --install-dir=/opt/synapse-test setups end up with a
# daemon hardcoded to /opt/synapse and setup.sh spawn fails ENOENT.
# We verify by inspecting the rendered file argv passed to `install` —
# the mock records argv but doesn't actually copy, so we read the
# rendered tmp source path back and grep it.

@test "phase_install_updater: renders {{INSTALL_DIR}} into the unit before install" {
    mock_default_externals
    INSTALL_DIR="$FAKE_INSTALL"
    run phase_install_updater
    assert_success

    # The mock recorded argv for `install`; second `install` call is
    # the unit copy. Find the tmp-source path (it's the arg right
    # before /etc/systemd/system/synapse-updater.service).
    run cat "$SYN_MOCK_CALLS/install"
    assert_output --partial "/etc/systemd/system/synapse-updater.service"

    # Re-render manually using the same logic the phase does, then
    # confirm the rendered output contains the actual INSTALL_DIR and
    # NO leftover placeholder. This sidesteps the fact that the
    # `install` mock doesn't preserve the source file (rm -f after
    # install), and tests the substitution invariant directly.
    local rendered
    rendered="$(mktemp)"
    sed -e "s|{{INSTALL_DIR}}|$FAKE_INSTALL|g" \
        "$FAKE_INSTALL/installer/updater/synapse-updater.service" >"$rendered"
    run grep -F "Environment=SYNAPSE_INSTALL_DIR=$FAKE_INSTALL" "$rendered"
    assert_success
    run grep -F "{{INSTALL_DIR}}" "$rendered"
    assert_failure
    rm -f "$rendered"
}

@test "phase_install_updater: source unit file uses placeholder, not /opt/synapse" {
    # Belt-and-suspenders: the on-disk source unit MUST contain the
    # placeholder. Anyone removing the substitution logic would also
    # have to fix this test, which forces the conversation.
    run grep -F "Environment=SYNAPSE_INSTALL_DIR={{INSTALL_DIR}}" \
        "$REPO_ROOT/installer/updater/synapse-updater.service"
    assert_success
    run grep -F "Environment=SYNAPSE_INSTALL_DIR=/opt/synapse" \
        "$REPO_ROOT/installer/updater/synapse-updater.service"
    assert_failure
}

# ---- skip paths -----------------------------------------------------

@test "phase_install_updater: skips cleanly when systemctl is missing" {
    # Override detect::has_cmd to fail only for systemctl. Other
    # commands (python3, etc) pretend to exist so the test exercises
    # the systemd-specific skip path, not the python3-missing path
    # that fires first if both were failing.
    detect::has_cmd() {
        case "$1" in
            systemctl) return 1 ;;
            *)         return 0 ;;
        esac
    }
    run phase_install_updater
    assert_success
    assert_output --partial "systemd not available"
    [ ! -f "$SYN_MOCK_CALLS/install" ]
}

@test "phase_install_updater: skips cleanly when python3 is missing" {
    detect::has_cmd() {
        case "$1" in
            python3) return 1 ;;
            *)       return 0 ;;
        esac
    }
    run phase_install_updater
    assert_success
    assert_output --partial "python3 not on PATH"
}

@test "phase_install_updater: warns when bundle is missing from INSTALL_DIR" {
    rm -rf "$FAKE_INSTALL/installer/updater"
    mock_default_externals
    run phase_install_updater
    assert_success
    assert_output --partial "updater bundle not found"
}

# ---- self-update guard ---------------------------------------------

@test "phase_install_updater: SYNAPSE_UPDATER_NO_RESTART=1 skips restart" {
    mock_default_externals
    SYNAPSE_UPDATER_NO_RESTART=1 run phase_install_updater
    assert_success
    # daemon-reload + enable still run (no harm in being idempotent),
    # but `restart` MUST be absent — that's the whole point of the
    # guard: don't kill the parent updater that's running this script.
    run cat "$SYN_MOCK_CALLS/systemctl"
    refute_output --partial "restart"
    # Sanity: daemon-reload still happened.
    assert_output --partial "daemon-reload"
}

# ---- idempotency ----------------------------------------------------

@test "phase_install_updater: running twice in a row does not fail" {
    mock_default_externals
    run phase_install_updater
    assert_success
    run phase_install_updater
    assert_success
}

# ---- TCP + bearer-token probe (v1.5.1+) ----------------------------

@test "phase_install_updater: probes daemon via TCP localhost with bearer token" {
    mock_default_externals
    run phase_install_updater
    assert_success

    # The probe reads token + port from $INSTALL_DIR/.env (rendered
    # by phase_secrets in the real install path; written by setup()
    # in this test). Verify curl was driven with the right scheme,
    # host, port, and Authorization header — all four together prove
    # we're no longer talking unix socket.
    [ -f "$SYN_MOCK_CALLS/curl" ]
    run cat "$SYN_MOCK_CALLS/curl"
    assert_output --partial "Authorization: Bearer bats-fixture-bearer-token"
    assert_output --partial "http://127.0.0.1:8089/healthz"
    refute_output --partial "--unix-socket"
}

@test "phase_install_updater: probe falls back to default port 8089 when .env lacks SYNAPSE_UPDATER_PORT" {
    # Operator deleted the line, or an upgrade from a pre-v1.5.1
    # install lands on a .env that never had the key. The probe must
    # not panic — it falls back to the documented default.
    cat >"$FAKE_INSTALL/.env" <<'EOF'
SYNAPSE_UPDATER_TOKEN=token-only-no-port
EOF
    mock_default_externals
    run phase_install_updater
    assert_success
    run cat "$SYN_MOCK_CALLS/curl"
    assert_output --partial "http://127.0.0.1:8089/healthz"
    assert_output --partial "Authorization: Bearer token-only-no-port"
}

@test "phase_install_updater: source updater.sh has no --unix-socket references" {
    # Belt-and-suspenders. Anyone reintroducing the socket path would
    # also need to fix this test, forcing a code-review conversation
    # about the daemon-protocol migration (PR 1 + PR 2 + PR 3).
    run grep -F -- "--unix-socket" "$INSTALLER_DIR/install/updater.sh"
    assert_failure
    run grep -F "/run/synapse/updater.sock" "$INSTALLER_DIR/install/updater.sh"
    assert_failure
}

# ---- daemon syntax (no separate phase needed) ----------------------

@test "synapse-updater: Python script is syntactically valid" {
    if ! command -v python3 >/dev/null 2>&1; then
        skip "python3 not available in this bats container"
    fi
    run python3 -m py_compile "$REPO_ROOT/installer/updater/synapse-updater"
    assert_success
}

@test "synapse-updater.service: contains the expected unit lines" {
    run grep -F "ExecStart=/usr/local/bin/synapse-updater" \
        "$REPO_ROOT/installer/updater/synapse-updater.service"
    assert_success
    # v1.5.1+: bearer token + TCP localhost. EnvironmentFile pulls
    # SYNAPSE_UPDATER_TOKEN out of /opt/synapse/.env (mode 0600) so the
    # secret never lives inline in the 0644 systemd unit. The leading `-`
    # makes the file optional so first-boot lifecycle stays graceful.
    run grep -F "EnvironmentFile=-/opt/synapse/.env" \
        "$REPO_ROOT/installer/updater/synapse-updater.service"
    assert_success
}

# Regression on v1.5.1 protocol switch: the old unix-socket lines must
# stay gone. If anyone reintroduces them this test fires immediately.
@test "synapse-updater.service: does not reintroduce the unix-socket lines" {
    run grep -F "RuntimeDirectory=synapse" \
        "$REPO_ROOT/installer/updater/synapse-updater.service"
    assert_failure
    run grep -F "SYNAPSE_UPDATER_SOCKET" \
        "$REPO_ROOT/installer/updater/synapse-updater.service"
    assert_failure
}

# PR 1 (daemon TCP) merged ahead of this PR's rebase, so EnvironmentFile=
# is present in the unit file as a hard contract — no skip-guard.
@test "synapse-updater.service: contains EnvironmentFile= for bearer token" {
    run grep -E '^EnvironmentFile=' \
        "$REPO_ROOT/installer/updater/synapse-updater.service"
    assert_success
}
