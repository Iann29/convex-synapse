#!/usr/bin/env bats
#
# Unit tests for installer/install/wizard.sh.
#
# Strategy: source ui.sh + wizard.sh, then exercise the pieces that
# DON'T require /dev/tty (should_run gates, validators, helpers).
#
# The interactive prompts (ask_yn, ask_choice, ask_text) read from
# /dev/tty directly so a curl|bash invocation works. Mocking /dev/tty
# from inside bats is fragile (would need an expect-script or pty
# wrapper); the prompts get end-to-end coverage via the real-VPS
# smoke that runs on every PR touching this file.

load '../helpers/load'

setup() {
    synapse_mock_setup
    # shellcheck source=../../install/ui.sh
    source "$INSTALLER_DIR/install/ui.sh"
    # shellcheck source=../../install/wizard.sh
    source "$INSTALLER_DIR/install/wizard.sh"
    UI_NO_COLOR=1
    UI_FORCE_COLOR=0
    # Reset all the globals wizard::run touches so the should_run
    # tests start from a clean slate.
    unset SYNAPSE_NON_INTERACTIVE NON_INTERACTIVE
    unset DOMAIN BASE_DOMAIN NO_TLS ENABLE_HA INSTALL_DIR
    unset SYNAPSE_AUTO_INSTALL_DOCKER
}

# ---- wizard::should_run -- gates ------------------------------------

@test "should_run: clean slate with /dev/tty available -> run" {
    if ! [[ -r /dev/tty ]]; then
        skip "no /dev/tty in this test environment"
    fi
    run wizard::should_run
    assert_success
}

@test "should_run: SYNAPSE_NON_INTERACTIVE=1 -> skip" {
    export SYNAPSE_NON_INTERACTIVE=1
    run wizard::should_run
    assert_failure
}

@test "should_run: NON_INTERACTIVE=1 -> skip" {
    export NON_INTERACTIVE=1
    run wizard::should_run
    assert_failure
}

@test "should_run: DOMAIN already set by flag -> skip" {
    export DOMAIN="synapse.example.com"
    run wizard::should_run
    assert_failure
}

@test "should_run: NO_TLS=1 (--no-tls flag) -> skip" {
    export NO_TLS=1
    run wizard::should_run
    assert_failure
}

@test "should_run: BASE_DOMAIN set -> skip" {
    export BASE_DOMAIN="apps.example.com"
    run wizard::should_run
    assert_failure
}

@test "should_run: ENABLE_HA=1 -> skip" {
    export ENABLE_HA=1
    run wizard::should_run
    assert_failure
}

# ---- wizard::valid_domain -------------------------------------------

@test "valid_domain: simple FQDN passes" {
    run wizard::valid_domain "synapse.example.com"
    assert_success
}

@test "valid_domain: long subdomain passes" {
    run wizard::valid_domain "a.b.c.d.example.co.uk"
    assert_success
}

@test "valid_domain: empty fails" {
    run wizard::valid_domain ""
    assert_failure
}

@test "valid_domain: missing dot (single label) fails" {
    run wizard::valid_domain "localhost"
    assert_failure
}

@test "valid_domain: leading dot fails" {
    run wizard::valid_domain ".example.com"
    assert_failure
}

@test "valid_domain: space inside fails" {
    run wizard::valid_domain "bad domain.com"
    assert_failure
}

@test "valid_domain: trailing dot fails (root anchor not accepted)" {
    run wizard::valid_domain "example.com."
    assert_failure
}

# ---- wizard::valid_path ---------------------------------------------

@test "valid_path: typical install dir passes" {
    run wizard::valid_path "/opt/synapse"
    assert_success
}

@test "valid_path: long absolute path passes" {
    run wizard::valid_path "/var/lib/synapse-test"
    assert_success
}

@test "valid_path: bare slash fails (root install would be insane)" {
    run wizard::valid_path "/"
    assert_failure
}

@test "valid_path: relative path fails" {
    run wizard::valid_path "synapse"
    assert_failure
}

@test "valid_path: tilde-prefixed path fails (no shell-expansion in our flow)" {
    run wizard::valid_path "~/synapse"
    assert_failure
}

@test "valid_path: empty fails" {
    run wizard::valid_path ""
    assert_failure
}

# ---- wizard::install_docker ----------------------------------------

@test "install_docker: aborts with fail when curl is missing" {
    # Override command -v to claim curl is absent.
    command() {
        if [[ "$1" == "-v" ]] && [[ "$2" == "curl" ]]; then
            return 1
        fi
        builtin command "$@"
    }
    run wizard::install_docker
    assert_failure 2
    assert_output --partial "curl not on PATH"
}

# ---- wizard::install_docker_offer (semantics) -----------------------
#
# install_docker_offer reads from /dev/tty so we can't drive ask_yn
# directly here. We exercise should_run gating in detail above; the
# end-to-end Y-yes path is covered by the real-VPS smoke.
#
# What we CAN cabling-check is that the function exists, takes no
# args, and the help text it prints when called is well-formed.
@test "install_docker_offer: function exists and is callable" {
    run declare -F wizard::install_docker_offer
    assert_success
    assert_output --partial "wizard::install_docker_offer"
}
