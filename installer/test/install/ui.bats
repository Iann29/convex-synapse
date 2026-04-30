#!/usr/bin/env bats
#
# Unit tests for installer/install/ui.sh.
#
# Color tests force UI_NO_COLOR=1 / UI_FORCE_COLOR=1 rather than
# relying on whether the bats process happens to have a TTY (it
# doesn't — bats redirects stdout to capture it). Spinner tests run
# in non-TTY mode where the spinner falls back to info+success/fail
# pairs, which is what's actually testable.

bats_require_minimum_version 1.5.0

load '../helpers/load'

setup() {
    synapse_mock_setup
    # shellcheck source=../../install/ui.sh
    source "$INSTALLER_DIR/install/ui.sh"
}

# ---- success / warn / fail / info -----------------------------------

@test "success: prints checkmark + message" {
    UI_NO_COLOR=1 run ui::success "Docker installed"
    assert_success
    assert_output "✓ Docker installed"
}

@test "warn: prints exclamation + message" {
    UI_NO_COLOR=1 run ui::warn "low disk"
    assert_success
    assert_output "! low disk"
}

@test "fail: writes to stderr, prefixed with X" {
    UI_NO_COLOR=1 run --separate-stderr ui::fail "out of memory"
    assert_success
    [ -z "$output" ]
    [ "$stderr" = "✗ out of memory" ]
}

@test "info: prints info glyph + message" {
    UI_NO_COLOR=1 run ui::info "running preflight"
    assert_success
    assert_output "ℹ running preflight"
}

@test "step: emits leading blank line + arrow + message" {
    # bats strips trailing empty $lines[] entries so we check the raw
    # $output for the leading newline; assertions on $lines[0] would
    # see "==>" because bats drops the empty entry that printf '\n'
    # produced.
    UI_NO_COLOR=1 run ui::step "Step 1: preflight"
    assert_success
    [[ "$output" == $'\n==> Step 1: preflight' ]]
    assert_line "==> Step 1: preflight"
}

# ---- color toggles --------------------------------------------------

@test "NO_COLOR (xdg standard) suppresses ANSI" {
    NO_COLOR=1 run ui::success "ok"
    assert_success
    assert_output "✓ ok"
    refute_output --partial $'\033'
}

@test "UI_FORCE_COLOR=1 enables ANSI even when not on TTY" {
    UI_FORCE_COLOR=1 run ui::success "ok"
    assert_success
    assert_output --partial $'\033[32m'
    assert_output --partial "ok"
}

@test "UI_NO_COLOR beats UI_FORCE_COLOR" {
    UI_NO_COLOR=1 UI_FORCE_COLOR=1 run ui::success "ok"
    assert_success
    refute_output --partial $'\033'
}

# ---- confirm --------------------------------------------------------

@test "confirm: SYNAPSE_NON_INTERACTIVE=1 + default Y -> success" {
    SYNAPSE_NON_INTERACTIVE=1 run ui::confirm "really?" Y
    assert_success
}

@test "confirm: SYNAPSE_NON_INTERACTIVE=1 + default N -> failure" {
    SYNAPSE_NON_INTERACTIVE=1 run ui::confirm "really?" N
    assert_failure
}

@test "confirm: SYNAPSE_NON_INTERACTIVE=1 + no default -> failure (default N)" {
    SYNAPSE_NON_INTERACTIVE=1 run ui::confirm "really?"
    assert_failure
}

@test "confirm: empty answer falls back to default Y" {
    SYNAPSE_NON_INTERACTIVE="" run bash -c "
        source '$INSTALLER_DIR/install/ui.sh'
        echo '' | ui::confirm 'really?' Y
    "
    assert_success
}

@test "confirm: 'y' answer succeeds regardless of default N" {
    SYNAPSE_NON_INTERACTIVE="" run bash -c "
        source '$INSTALLER_DIR/install/ui.sh'
        echo 'y' | ui::confirm 'really?' N
    "
    assert_success
}

@test "confirm: 'n' answer fails regardless of default Y" {
    SYNAPSE_NON_INTERACTIVE="" run bash -c "
        source '$INSTALLER_DIR/install/ui.sh'
        echo 'n' | ui::confirm 'really?' Y
    "
    assert_failure
}

# ---- spin (non-TTY mode) --------------------------------------------
#
# In a real TTY ui::spin animates a Braille spinner. Bats redirects
# stdout, so [[ -t 1 ]] is false and we exercise the off-TTY branch:
# info → run → success/fail.

@test "spin: command success -> info + success messages" {
    UI_NO_COLOR=1 run ui::spin "pulling image" true
    assert_success
    assert_line "ℹ pulling image"
    assert_line "✓ pulling image"
}

@test "spin: command failure -> info + fail with exit code on stderr" {
    UI_NO_COLOR=1 run --separate-stderr ui::spin "bad step" bash -c "exit 7"
    assert_failure 7
    [[ "$output" == *"ℹ bad step"* ]]
    [[ "$stderr" == *"✗ bad step (exit 7)"* ]]
}

@test "spin: passes args to command verbatim" {
    UI_NO_COLOR=1 run ui::spin "echo test" echo "hello world"
    assert_success
    assert_line --partial "hello world"
}
