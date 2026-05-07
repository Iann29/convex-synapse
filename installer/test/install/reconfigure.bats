#!/usr/bin/env bats
#
# Unit tests for `setup.sh --reconfigure` and the underlying
# `lifecycle::reconfigure` function. Same mocking pattern as
# lifecycle.bats: PATH-shadow `docker` so we don't need a real engine.
#
# What we cover (matches the task contract):
#   - flag parsing surfaces the right error code on bad combos
#   - install-dir preflight (`not_installed`)
#   - domain regex validation (`invalid_domain`)
#   - .env updates the documented keys + preserves the others
#   - --no-tls strips SYNAPSE_DOMAIN + sets PUBLIC_URL appropriately
#   - second --reconfigure call with same flags is a clean no-op
#   - caddy validation failure leaves .env / Caddyfile untouched

bats_require_minimum_version 1.5.0

load '../helpers/load'

setup() {
    synapse_mock_setup
    # shellcheck source=../../install/ui.sh
    source "$INSTALLER_DIR/install/ui.sh"
    # shellcheck source=../../install/secrets.sh
    source "$INSTALLER_DIR/install/secrets.sh"
    # shellcheck source=../../install/caddy.sh
    source "$INSTALLER_DIR/install/caddy.sh"
    # shellcheck source=../../install/compose.sh
    source "$INSTALLER_DIR/install/compose.sh"
    # shellcheck source=../../lib/detect.sh
    source "$INSTALLER_DIR/lib/detect.sh"
    # shellcheck source=../../install/lifecycle.sh
    source "$INSTALLER_DIR/install/lifecycle.sh"
    UI_NO_COLOR=1
    INSTALL_DIR="$BATS_TEST_TMPDIR/install"
    mkdir -p "$INSTALL_DIR"
    ENV_FILE="$INSTALL_DIR/.env"
    COMPOSE_FILE="$INSTALL_DIR/docker-compose.yml"
    INSTALLER_TEMPLATES="$INSTALLER_DIR/templates"
    export INSTALLER_TEMPLATES

    # Default-pass docker mock so any compose/restart shell-out succeeds.
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    COMPOSE_CMD="$SYN_MOCK_BIN/docker"
    export COMPOSE_CMD

    # Default-pass caddy validate stub. Tests that need failure can
    # override with their own RECONFIGURE_VALIDATE_CMD.
    cat >"$SYN_MOCK_BIN/caddy_ok" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/caddy_ok"
    RECONFIGURE_VALIDATE_CMD="$SYN_MOCK_BIN/caddy_ok"
    export RECONFIGURE_VALIDATE_CMD
}

# Helper: minimal install fixture — just enough for reconfigure to
# pass the install-dir preflight without complaints.
_install_fixture() {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=1.3.0
SYNAPSE_JWT_SECRET=must-be-preserved-jwt
POSTGRES_PASSWORD=must-be-preserved-pg
SYNAPSE_PORT=8080
DASHBOARD_PORT=6790
SYNAPSE_PUBLIC_URL=http://1.2.3.4:8080
SYNAPSE_ALLOWED_ORIGINS=*
EOF
    cat >"$COMPOSE_FILE" <<EOF
services:
  synapse: {}
EOF
}

# ---- flag-combo validation -----------------------------------------

@test "reconfigure: --domain + --no-tls → bad_flags" {
    _install_fixture
    run lifecycle::reconfigure "$INSTALL_DIR" --domain=foo.com --no-tls
    assert_failure 2
    assert_output --partial "bad_flags"
    assert_output --partial "--domain and --no-tls cannot be combined"
}

@test "reconfigure: no flags → bad_flags" {
    _install_fixture
    run lifecycle::reconfigure "$INSTALL_DIR"
    assert_failure 2
    assert_output --partial "bad_flags"
    assert_output --partial "at least one of"
}

# ---- install-dir preflight -----------------------------------------

@test "reconfigure: missing install dir → not_installed" {
    local bogus="$BATS_TEST_TMPDIR/no-such"
    mkdir -p "$bogus"
    run lifecycle::reconfigure "$bogus" --domain=foo.com
    assert_failure 2
    assert_output --partial "not_installed"
}

@test "reconfigure: install dir without compose file → not_installed" {
    : >"$ENV_FILE"
    run lifecycle::reconfigure "$INSTALL_DIR" --domain=foo.com
    assert_failure 2
    assert_output --partial "not_installed"
}

# ---- domain validation ---------------------------------------------

@test "reconfigure: invalid domain string → invalid_domain" {
    _install_fixture
    run lifecycle::reconfigure "$INSTALL_DIR" --domain='invalid_domain!'
    assert_failure 2
    assert_output --partial "invalid_domain"
}

@test "reconfigure: domain without a dot → invalid_domain" {
    _install_fixture
    run lifecycle::reconfigure "$INSTALL_DIR" --domain='localhost'
    assert_failure 2
    assert_output --partial "invalid_domain"
}

@test "reconfigure: invalid base-domain → invalid_domain" {
    _install_fixture
    run lifecycle::reconfigure "$INSTALL_DIR" --base-domain='not_a_domain!'
    assert_failure 2
    assert_output --partial "invalid_domain"
}

@test "reconfigure: malformed --acme-email → invalid_email" {
    _install_fixture
    run lifecycle::reconfigure "$INSTALL_DIR" --domain=foo.com --acme-email='not-an-email'
    assert_failure 2
    assert_output --partial "invalid_email"
}

# ---- happy paths ---------------------------------------------------

@test "reconfigure: --domain=foo.com updates SYNAPSE_DOMAIN + SYNAPSE_PUBLIC_URL" {
    _install_fixture
    run lifecycle::reconfigure "$INSTALL_DIR" --domain=foo.com
    assert_success
    assert_output --partial "Reconfigured"

    run secrets::env_get "$ENV_FILE" SYNAPSE_DOMAIN
    assert_output "foo.com"
    run secrets::env_get "$ENV_FILE" SYNAPSE_PUBLIC_URL
    assert_output "https://foo.com"
    run secrets::env_get "$ENV_FILE" PUBLIC_SYNAPSE_URL
    assert_output "https://foo.com"
    run secrets::env_get "$ENV_FILE" SYNAPSE_ALLOWED_ORIGINS
    assert_output "https://foo.com"

    # Secrets preserved verbatim.
    run secrets::env_get "$ENV_FILE" SYNAPSE_JWT_SECRET
    assert_output "must-be-preserved-jwt"
    run secrets::env_get "$ENV_FILE" POSTGRES_PASSWORD
    assert_output "must-be-preserved-pg"

    # Caddyfile rendered.
    [ -f "$INSTALL_DIR/Caddyfile" ]
    run cat "$INSTALL_DIR/Caddyfile"
    assert_output --partial "foo.com"
}

@test "reconfigure: --no-tls strips SYNAPSE_DOMAIN" {
    _install_fixture
    # Pre-seed SYNAPSE_DOMAIN so we can prove --no-tls clears it.
    secrets::set_env_var "$ENV_FILE" SYNAPSE_DOMAIN "old.example.com"
    secrets::set_env_var "$ENV_FILE" SYNAPSE_ACME_EMAIL "old@example.com"

    run lifecycle::reconfigure "$INSTALL_DIR" --no-tls
    assert_success

    run secrets::env_get "$ENV_FILE" SYNAPSE_DOMAIN
    assert_output ""
    run secrets::env_get "$ENV_FILE" SYNAPSE_ACME_EMAIL
    assert_output ""
    # No Caddyfile written when going to plain HTTP without a base-domain.
    [ ! -f "$INSTALL_DIR/Caddyfile" ]
}

@test "reconfigure: --base-domain=apps.foo.com sets SYNAPSE_BASE_DOMAIN" {
    _install_fixture
    secrets::set_env_var "$ENV_FILE" SYNAPSE_DOMAIN "foo.com"
    secrets::set_env_var "$ENV_FILE" SYNAPSE_ACME_EMAIL "admin@foo.com"

    run lifecycle::reconfigure "$INSTALL_DIR" --base-domain=apps.foo.com
    assert_success

    run secrets::env_get "$ENV_FILE" SYNAPSE_BASE_DOMAIN
    assert_output "apps.foo.com"
    # Existing SYNAPSE_DOMAIN preserved (we didn't pass --domain).
    run secrets::env_get "$ENV_FILE" SYNAPSE_DOMAIN
    assert_output "foo.com"

    # Caddyfile includes the wildcard block.
    [ -f "$INSTALL_DIR/Caddyfile" ]
    run cat "$INSTALL_DIR/Caddyfile"
    assert_output --partial "*.apps.foo.com"
}

@test "reconfigure: --base-domain strips leading dot" {
    _install_fixture
    secrets::set_env_var "$ENV_FILE" SYNAPSE_DOMAIN "foo.com"
    secrets::set_env_var "$ENV_FILE" SYNAPSE_ACME_EMAIL "admin@foo.com"

    run lifecycle::reconfigure "$INSTALL_DIR" --base-domain=.apps.foo.com
    assert_success

    run secrets::env_get "$ENV_FILE" SYNAPSE_BASE_DOMAIN
    assert_output "apps.foo.com"
}

@test "reconfigure: --acme-email overrides default" {
    _install_fixture
    run lifecycle::reconfigure "$INSTALL_DIR" --domain=foo.com --acme-email=ops@elsewhere.com
    assert_success

    run secrets::env_get "$ENV_FILE" SYNAPSE_ACME_EMAIL
    assert_output "ops@elsewhere.com"
}

# ---- idempotency ---------------------------------------------------

@test "reconfigure: running twice with same args is a clean no-op" {
    _install_fixture
    run lifecycle::reconfigure "$INSTALL_DIR" --domain=foo.com
    assert_success

    # Capture state after first run.
    local domain_after_first public_after_first caddy_md5_first
    domain_after_first="$(secrets::env_get "$ENV_FILE" SYNAPSE_DOMAIN)"
    public_after_first="$(secrets::env_get "$ENV_FILE" SYNAPSE_PUBLIC_URL)"
    caddy_md5_first="$(md5sum "$INSTALL_DIR/Caddyfile" 2>/dev/null | awk '{print $1}')"

    run lifecycle::reconfigure "$INSTALL_DIR" --domain=foo.com
    assert_success

    # State after second run should match.
    run secrets::env_get "$ENV_FILE" SYNAPSE_DOMAIN
    assert_output "$domain_after_first"
    run secrets::env_get "$ENV_FILE" SYNAPSE_PUBLIC_URL
    assert_output "$public_after_first"
    local caddy_md5_second
    caddy_md5_second="$(md5sum "$INSTALL_DIR/Caddyfile" 2>/dev/null | awk '{print $1}')"
    [ "$caddy_md5_first" = "$caddy_md5_second" ]
}

# ---- caddy validation failure leaves files untouched ---------------

@test "reconfigure: caddy validation failure aborts with files untouched" {
    _install_fixture

    # Stub a validator that always fails.
    cat >"$SYN_MOCK_BIN/caddy_bad" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
    chmod +x "$SYN_MOCK_BIN/caddy_bad"

    # Capture original .env content for byte-comparison after the failed run.
    local before_env_md5
    before_env_md5="$(md5sum "$ENV_FILE" | awk '{print $1}')"

    RECONFIGURE_VALIDATE_CMD="$SYN_MOCK_BIN/caddy_bad" \
        run lifecycle::reconfigure "$INSTALL_DIR" --domain=foo.com
    assert_failure 2
    assert_output --partial "caddy_validation_failed"

    # .env unchanged.
    local after_env_md5
    after_env_md5="$(md5sum "$ENV_FILE" | awk '{print $1}')"
    [ "$before_env_md5" = "$after_env_md5" ]

    # No Caddyfile in place (the staged tmp got cleaned up by the wrapper).
    [ ! -f "$INSTALL_DIR/Caddyfile" ]
}

# ---- audit log -----------------------------------------------------

@test "reconfigure: appends to reconfigure.log on success" {
    _install_fixture
    run lifecycle::reconfigure "$INSTALL_DIR" --domain=foo.com
    assert_success
    [ -f "$INSTALL_DIR/reconfigure.log" ]
    run cat "$INSTALL_DIR/reconfigure.log"
    assert_output --partial "old:"
    assert_output --partial "new:"
    assert_output --partial "domain=foo.com"
}

# ---- setup.sh --reconfigure flag wiring ----------------------------
#
# These exercise the parse_flags + main() early-return branch wired
# into setup.sh. We only need to prove the flag is parsed and the
# function is reached — the deep behaviour is covered above.

@test "setup.sh --reconfigure: complains when install dir has no .env" {
    local fake_dir="$BATS_TEST_TMPDIR/empty-reconfigure"
    mkdir -p "$fake_dir"
    REPO_ROOT="$(cd "$INSTALLER_DIR/.." && pwd)"
    SETUP="$REPO_ROOT/setup.sh"
    [ -x "$SETUP" ]
    run "$SETUP" --reconfigure --domain=foo.com --install-dir="$fake_dir"
    assert_failure 2
    assert_output --partial "not_installed"
}

@test "setup.sh --reconfigure with no payload flag → bad_flags" {
    REPO_ROOT="$(cd "$INSTALLER_DIR/.." && pwd)"
    SETUP="$REPO_ROOT/setup.sh"
    [ -x "$SETUP" ]
    # Need an install dir that exists so we get past the install-dir
    # check and reach the flag-combo check.
    _install_fixture
    run "$SETUP" --reconfigure --install-dir="$INSTALL_DIR"
    assert_failure 2
    assert_output --partial "bad_flags"
}

@test "setup.sh --help: lists --reconfigure" {
    REPO_ROOT="$(cd "$INSTALLER_DIR/.." && pwd)"
    SETUP="$REPO_ROOT/setup.sh"
    [ -x "$SETUP" ]
    run "$SETUP" --help
    assert_success
    assert_output --partial "--reconfigure"
}

@test "parse_flags: --reconfigure sets RECONFIGURE=1" {
    REPO_ROOT="$(cd "$INSTALLER_DIR/.." && pwd)"
    SETUP="$REPO_ROOT/setup.sh"
    [ -x "$SETUP" ]
    run bash -c "
        __SETUP_NO_MAIN=1 source '$SETUP'
        parse_flags --reconfigure
        echo \"RC=\$RECONFIGURE\"
    "
    assert_success
    assert_output --partial "RC=1"
}
