#!/usr/bin/env bats
#
# Unit tests for installer/install/caddy.sh.
#
# Strategy: the upsert/remove/render/write_standalone primitives are
# pure shell — we test them on $BATS_TEST_TMPDIR fixtures with no
# external dependencies. detect_mode is exercised by overriding
# detect::has_caddy / detect::has_nginx via shell-function
# redefinition. install_host_block injects CADDY_RELOAD with a stub
# binary so the systemctl reload doesn't actually run.

bats_require_minimum_version 1.5.0

load '../helpers/load'

setup() {
    synapse_mock_setup
    # shellcheck source=../../lib/detect.sh
    source "$INSTALLER_DIR/lib/detect.sh"
    # shellcheck source=../../install/caddy.sh
    source "$INSTALLER_DIR/install/caddy.sh"
    INSTALLER_TEMPLATES="$INSTALLER_DIR/templates"
    export INSTALLER_TEMPLATES
    CADDY_FILE="$BATS_TEST_TMPDIR/Caddyfile"
}

# ---- upsert_block ---------------------------------------------------

@test "upsert_block: writes BEGIN/END wrapped block on a fresh file" {
    echo "synapse-block-content" | caddy::upsert_block "$CADDY_FILE" synapse
    [ -f "$CADDY_FILE" ]
    run grep -c "^# BEGIN synapse" "$CADDY_FILE"
    assert_output "1"
    run grep -c "^# END synapse" "$CADDY_FILE"
    assert_output "1"
    run grep -c "synapse-block-content" "$CADDY_FILE"
    assert_output "1"
}

@test "upsert_block: re-running replaces previous block (no duplicates)" {
    echo "first-version" | caddy::upsert_block "$CADDY_FILE" synapse
    echo "second-version" | caddy::upsert_block "$CADDY_FILE" synapse
    run grep -c "^# BEGIN synapse" "$CADDY_FILE"
    assert_output "1"
    run grep -c "first-version" "$CADDY_FILE"
    assert_output "0"
    run grep -c "second-version" "$CADDY_FILE"
    assert_output "1"
}

@test "upsert_block: preserves operator's other content" {
    cat >"$CADDY_FILE" <<'EOF'
example.com {
    reverse_proxy localhost:8081
}
EOF
    echo "synapse-stuff" | caddy::upsert_block "$CADDY_FILE" synapse
    run grep -c "example.com" "$CADDY_FILE"
    assert_output "1"
    run grep -c "synapse-stuff" "$CADDY_FILE"
    assert_output "1"
}

@test "upsert_block: multiple tags coexist" {
    echo "synapse" | caddy::upsert_block "$CADDY_FILE" synapse
    echo "other" | caddy::upsert_block "$CADDY_FILE" other-app
    run grep -c "^# BEGIN synapse" "$CADDY_FILE"
    assert_output "1"
    run grep -c "^# BEGIN other-app" "$CADDY_FILE"
    assert_output "1"
}

# ---- remove_block ---------------------------------------------------

@test "remove_block: drops the named block, leaves rest" {
    cat >"$CADDY_FILE" <<'EOF'
example.com {
    reverse_proxy localhost:8081
}
EOF
    echo "synapse-stuff" | caddy::upsert_block "$CADDY_FILE" synapse
    caddy::remove_block "$CADDY_FILE" synapse
    run grep -c "synapse-stuff" "$CADDY_FILE"
    assert_output "0"
    run grep -c "^# BEGIN synapse" "$CADDY_FILE"
    assert_output "0"
    run grep -c "example.com" "$CADDY_FILE"
    assert_output "1"
}

@test "remove_block: missing file is a no-op" {
    run caddy::remove_block /nonexistent/Caddyfile synapse
    assert_success
}

# ---- detect_mode ---------------------------------------------------

@test "detect_mode: caddy on PATH -> caddy_host" {
    detect::has_caddy() { return 0; }
    detect::has_nginx() { return 1; }
    run caddy::detect_mode
    assert_success
    assert_output "caddy_host"
}

@test "detect_mode: nginx but no caddy -> nginx_external" {
    detect::has_caddy() { return 1; }
    detect::has_nginx() { return 0; }
    run caddy::detect_mode
    assert_success
    assert_output "nginx_external"
}

@test "detect_mode: neither -> caddy_compose (we'll bring our own)" {
    detect::has_caddy() { return 1; }
    detect::has_nginx() { return 1; }
    run caddy::detect_mode
    assert_success
    assert_output "caddy_compose"
}

@test "detect_mode: CADDY_FORCE_MODE override applies" {
    detect::has_caddy() { return 0; }
    CADDY_FORCE_MODE=caddy_compose run caddy::detect_mode
    assert_success
    assert_output "caddy_compose"
}

# ---- _render --------------------------------------------------------

@test "_render: substitutes {{KEY}} from exported env" {
    local tmpl="$BATS_TEST_TMPDIR/x.tmpl"
    cat >"$tmpl" <<'EOF'
{{DOMAIN}} { reverse_proxy localhost:{{PORT}} }
EOF
    DOMAIN=synapse.example.com PORT=8080 run caddy::_render "$tmpl"
    assert_success
    assert_output --partial "synapse.example.com"
    assert_output --partial "localhost:8080"
}

@test "_render: missing var becomes empty" {
    local tmpl="$BATS_TEST_TMPDIR/x.tmpl"
    cat >"$tmpl" <<'EOF'
A={{A}} B={{NOPE}}
EOF
    A=ok run caddy::_render "$tmpl"
    assert_success
    assert_output "A=ok B="
}

# ---- install_host_block --------------------------------------------

@test "install_host_block: renders fragment, upserts, calls reload stub" {
    detect::has_caddy() { return 0; }
    cat >"$SYN_MOCK_BIN/fakereload" <<'EOF'
#!/usr/bin/env bash
echo "reload-was-called" > "$BATS_TEST_TMPDIR/reload-marker"
EOF
    chmod +x "$SYN_MOCK_BIN/fakereload"
    DOMAIN=synapse.example.com DASHBOARD_PORT=6790 SYNAPSE_PORT=8080 \
    CADDY_RELOAD="$SYN_MOCK_BIN/fakereload" \
        caddy::install_host_block "$CADDY_FILE"
    [ -f "$CADDY_FILE" ]
    run grep -c "synapse.example.com" "$CADDY_FILE"
    assert_output "1"
    run grep -c "localhost:6790" "$CADDY_FILE"
    assert_output "1"
    [ -f "$BATS_TEST_TMPDIR/reload-marker" ]
}

@test "install_host_block: re-run replaces block, doesn't duplicate" {
    cat >"$SYN_MOCK_BIN/fakereload" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/fakereload"
    DOMAIN=v1.example.com DASHBOARD_PORT=6790 SYNAPSE_PORT=8080 \
    CADDY_RELOAD="$SYN_MOCK_BIN/fakereload" \
        caddy::install_host_block "$CADDY_FILE"
    DOMAIN=v2.example.com DASHBOARD_PORT=6790 SYNAPSE_PORT=8080 \
    CADDY_RELOAD="$SYN_MOCK_BIN/fakereload" \
        caddy::install_host_block "$CADDY_FILE"
    run grep -c "^# BEGIN synapse" "$CADDY_FILE"
    assert_output "1"
    run grep -c "v1.example.com" "$CADDY_FILE"
    assert_output "0"
    run grep -c "v2.example.com" "$CADDY_FILE"
    assert_output "1"
}

# ---- print_nginx_snippet -------------------------------------------

@test "print_nginx_snippet: emits proxy_pass for each location" {
    SYNAPSE_PORT=8080 DASHBOARD_PORT=6790 run caddy::print_nginx_snippet
    assert_success
    assert_output --partial "/v1/"
    assert_output --partial "/d/"
    assert_output --partial "/health"
    assert_output --partial "127.0.0.1:8080"
    assert_output --partial "127.0.0.1:6790"
}

# ---- write_standalone ----------------------------------------------

@test "write_standalone: renders template to fresh path" {
    local out="$BATS_TEST_TMPDIR/Caddyfile.standalone"
    DOMAIN=synapse.example.com ACME_EMAIL=ops@example.com \
    DASHBOARD_PORT=6790 SYNAPSE_PORT=8080 \
    SYNAPSE_BASE_DOMAIN= \
        caddy::write_standalone "$out"
    [ -f "$out" ]
    run grep -c "synapse.example.com" "$out"
    assert_output "1"
    run grep -c "ops@example.com" "$out"
    assert_output "1"
    # Compose mode uses container names, not localhost — so the
    # Caddy container in synapse-network can reach the upstream
    # services by service name. Two matches now: one for the main
    # site's reverse_proxy, one for the on_demand_tls ask URL in
    # the global block (the ask hook is harmless when no
    # on-demand site exists, so it stays on regardless).
    run grep -c "synapse-api:8080" "$out"
    assert_output "2"
    run grep -c "synapse-dashboard:3000" "$out"
    assert_output "1"
}

# ---- write_standalone + custom domains -----------------------------

@test "write_standalone: appends wildcard block when SYNAPSE_BASE_DOMAIN set" {
    local out="$BATS_TEST_TMPDIR/Caddyfile.wildcard"
    DOMAIN=synapse.example.com ACME_EMAIL=ops@example.com \
    DASHBOARD_PORT=6790 SYNAPSE_PORT=8080 \
    SYNAPSE_BASE_DOMAIN=synapse.example.com \
        caddy::write_standalone "$out"
    [ -f "$out" ]
    # Wildcard site block present
    run grep -c '^\*\.synapse.example.com {' "$out"
    assert_output "1"
    # On-demand TLS directive in the global block (matches once as
    # a directive plus once or more in template prose comments —
    # we just want at least one)
    run grep -E "^\s*on_demand_tls\s*\{" "$out"
    assert_success
    # Ask URL points at synapse-api's /v1/internal/tls_ask (only one
    # actual directive, even if comments mention it)
    run grep -E "^\s*ask\s+http://synapse-api:8080/v1/internal/tls_ask" "$out"
    assert_success
    # Wildcard reverse_proxy points at the synapse-api service
    run grep -c "reverse_proxy synapse-api:8080" "$out"
    assert_output "2"  # one for the main site's catch-all, one for the wildcard
}

@test "write_standalone: NO wildcard block when SYNAPSE_BASE_DOMAIN empty" {
    local out="$BATS_TEST_TMPDIR/Caddyfile.no-wildcard"
    DOMAIN=synapse.example.com ACME_EMAIL=ops@example.com \
    DASHBOARD_PORT=6790 SYNAPSE_PORT=8080 \
    SYNAPSE_BASE_DOMAIN= \
        caddy::write_standalone "$out"
    [ -f "$out" ]
    # No wildcard site
    run grep -c "^\*\." "$out"
    assert_output "0"
}

@test "write_standalone: refuses to overwrite without CADDY_FORCE_OVERWRITE" {
    local out="$BATS_TEST_TMPDIR/Caddyfile.standalone"
    : >"$out"
    DOMAIN=x ACME_EMAIL=x DASHBOARD_PORT=1 SYNAPSE_PORT=2 \
        run caddy::write_standalone "$out"
    assert_failure 1
    assert_output --partial "exists"
}

@test "write_standalone: CADDY_FORCE_OVERWRITE=1 replaces existing" {
    local out="$BATS_TEST_TMPDIR/Caddyfile.standalone"
    echo "old" >"$out"
    DOMAIN=fresh.example.com ACME_EMAIL=x ACME_EMAIL=x \
    DASHBOARD_PORT=6790 SYNAPSE_PORT=8080 \
    CADDY_FORCE_OVERWRITE=1 \
        caddy::write_standalone "$out"
    run grep -c "fresh.example.com" "$out"
    assert_output "1"
    run grep -c "^old$" "$out"
    assert_output "0"
}
