#!/usr/bin/env bats
#
# Smoke tests for setup.sh — the v0.6.0 orchestrator. Doesn't try to
# bring the real stack up (that needs Docker-in-Docker which is too
# slow for unit-test CI); instead exercises the parts that ARE
# testable in isolation:
#   - --help / --version output
#   - parse_flags branches
#   - flag rejection (unknown, --upgrade not-yet-impl, etc)
#   - the trap + lock + source_libs scaffolding
#
# Real end-to-end install tests against debian/ubuntu/fedora fixtures
# come in a follow-up: `installer/test/integration/` Dockerfiles +
# a CI job behind a BATS_RUN_INTEGRATION=1 gate so the cheap-and-
# fast suite stays fast.

bats_require_minimum_version 1.5.0

load '../helpers/load'

setup() {
    synapse_mock_setup
    REPO_ROOT="$(cd "$INSTALLER_DIR/.." && pwd)"
    SETUP="$REPO_ROOT/setup.sh"
    [ -x "$SETUP" ]
}

# ---- --version / --help / unknown flag -----------------------------

@test "setup.sh --version: prints installer version" {
    run "$SETUP" --version
    assert_success
    assert_output --partial "synapse-installer"
    # Match X.Y.Z without pinning to a specific value — tracking
    # INSTALLER_VERSION here just creates churn on every release.
    assert_output --regexp '[0-9]+\.[0-9]+\.[0-9]+'
}

@test "setup.sh --help: lists every flag" {
    run "$SETUP" --help
    assert_success
    assert_output --partial "--domain=<host>"
    assert_output --partial "--non-interactive"
    assert_output --partial "--doctor"
    assert_output --partial "--upgrade"
    assert_output --partial "--uninstall"
}

@test "setup.sh unknown flag -> exit 2 + usage on stderr" {
    run --separate-stderr "$SETUP" --not-a-real-flag
    assert_failure 2
    [[ "$stderr" == *"unknown flag"* ]]
}

# ---- --uninstall flag wiring ----------------------------------------

@test "setup.sh --uninstall: complains when install dir has no .env" {
    local fake_dir="$BATS_TEST_TMPDIR/empty-uninstall"
    mkdir -p "$fake_dir"
    run "$SETUP" --uninstall --non-interactive --install-dir="$fake_dir"
    assert_failure 2
    assert_output --partial "no Synapse install"
}

# ---- --upgrade flag wiring -----------------------------------------
#
# The full --upgrade flow is exercised in lifecycle.bats with mocked
# git/docker. Here we only confirm the flag is parsed and the
# missing-install error message reaches the operator.

@test "setup.sh --upgrade: complains when install dir has no .env" {
    local fake_dir="$BATS_TEST_TMPDIR/empty-install"
    mkdir -p "$fake_dir"
    run "$SETUP" --upgrade --install-dir="$fake_dir"
    assert_failure 2
    assert_output --partial "no .env"
}

# ---- source-mode probing -------------------------------------------
#
# Setting __SETUP_NO_MAIN=1 short-circuits the main() call so we can
# inspect the script's helper functions in a subshell without running
# preflight + compose.

@test "source: __SETUP_NO_MAIN skips main, exposes helpers" {
    run bash -c "
        __SETUP_NO_MAIN=1 source '$SETUP'
        # The functions should be defined.
        type parse_flags >/dev/null
        type usage >/dev/null
        type on_err >/dev/null
        type on_exit >/dev/null
        type acquire_lock >/dev/null
        echo OK
    "
    assert_success
    assert_output --partial "OK"
}

@test "parse_flags: --domain= sets DOMAIN" {
    run bash -c "
        __SETUP_NO_MAIN=1 source '$SETUP'
        parse_flags --domain=synapse.example.com
        echo \"DOMAIN=\$DOMAIN\"
    "
    assert_success
    assert_output --partial "DOMAIN=synapse.example.com"
}

@test "parse_flags: --acme-email= overrides default" {
    run bash -c "
        __SETUP_NO_MAIN=1 source '$SETUP'
        parse_flags --domain=x.example.com --acme-email=ops@example.com
        echo \"ACME=\$ACME_EMAIL\"
    "
    assert_success
    assert_output --partial "ACME=ops@example.com"
}

@test "parse_flags: --acme-email defaults to admin@<domain>" {
    run bash -c "
        __SETUP_NO_MAIN=1 source '$SETUP'
        parse_flags --domain=synapse.example.com
        echo \"ACME=\$ACME_EMAIL\"
    "
    assert_success
    assert_output --partial "ACME=admin@synapse.example.com"
}

@test "parse_flags: --enable-ha sets flag" {
    run bash -c "
        __SETUP_NO_MAIN=1 source '$SETUP'
        parse_flags --enable-ha
        echo \"HA=\$ENABLE_HA\"
    "
    assert_success
    assert_output --partial "HA=1"
}

@test "parse_flags: --no-tls + --skip-dns-check accumulate" {
    run bash -c "
        __SETUP_NO_MAIN=1 source '$SETUP'
        parse_flags --domain=x --no-tls --skip-dns-check
        echo \"NO_TLS=\$NO_TLS SKIP_DNS=\$SKIP_DNS\"
    "
    assert_success
    assert_output --partial "NO_TLS=1 SKIP_DNS=1"
}

@test "parse_flags: --non-interactive exports SYNAPSE_NON_INTERACTIVE" {
    run bash -c "
        __SETUP_NO_MAIN=1 source '$SETUP'
        parse_flags --non-interactive
        echo \"NI=\$SYNAPSE_NON_INTERACTIVE\"
    "
    assert_success
    assert_output --partial "NI=1"
}

@test "parse_flags: --install-dir= overrides default" {
    run bash -c "
        __SETUP_NO_MAIN=1 source '$SETUP'
        parse_flags --install-dir=/srv/synapse
        echo \"DIR=\$INSTALL_DIR\"
    "
    assert_success
    assert_output --partial "DIR=/srv/synapse"
}

# ---- bash -n (parse-only) ------------------------------------------

@test "setup.sh parses cleanly (bash -n)" {
    run bash -n "$SETUP"
    assert_success
}

@test "every install/*.sh parses cleanly" {
    for f in "$INSTALLER_DIR"/install/*.sh "$INSTALLER_DIR"/lib/*.sh; do
        run bash -n "$f"
        assert_success
    done
}
