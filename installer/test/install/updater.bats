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
    # along, none of which the phase needs.
    # shellcheck source=../../install/ui.sh
    source "$INSTALLER_DIR/install/ui.sh"
    # shellcheck source=../../lib/detect.sh
    source "$INSTALLER_DIR/lib/detect.sh"
    # shellcheck source=../../install/updater.sh
    source "$INSTALLER_DIR/install/updater.sh"

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
    run grep -F "RuntimeDirectory=synapse" \
        "$REPO_ROOT/installer/updater/synapse-updater.service"
    assert_success
}
