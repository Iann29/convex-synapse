#!/usr/bin/env bats
#
# Unit tests for installer/install/verify.sh.
#
# Mocks curl (per-endpoint responses) and jq via PATH-shadow. The
# verify::run end-to-end happy path is asserted by stitching the
# mocks into a state machine that returns the right body for each
# URL the script hits.

bats_require_minimum_version 1.5.0

load '../helpers/load'

setup() {
    synapse_mock_setup
    # shellcheck source=../../install/ui.sh
    source "$INSTALLER_DIR/install/ui.sh"
    # shellcheck source=../../install/verify.sh
    source "$INSTALLER_DIR/install/verify.sh"
    UI_NO_COLOR=1
    # Real jq is needed for response parsing — present in bats/bats:latest.
    VERIFY_JQ=jq
    export VERIFY_JQ
}

# ---- _curl + _jq helpers -------------------------------------------

@test "_curl: builds POST with json body and returns response" {
    mock_cmd curl 0 '{"ok":true}'
    VERIFY_CURL="$SYN_MOCK_BIN/curl" run verify::_curl POST http://x/api '{"a":1}'
    assert_success
    assert_output '{"ok":true}'
}

@test "_curl: adds Bearer header when VERIFY_TOKEN is set" {
    cat >"$SYN_MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
echo "$@" >"$BATS_TEST_TMPDIR/curl.args"
echo '{}'
EOF
    chmod +x "$SYN_MOCK_BIN/curl"
    VERIFY_CURL="$SYN_MOCK_BIN/curl" VERIFY_TOKEN=tok-123 run verify::_curl GET http://x
    assert_success
    run cat "$BATS_TEST_TMPDIR/curl.args"
    assert_output --partial "Authorization: Bearer tok-123"
}

# ---- register / create_team / create_project / create_deployment ---

@test "register: extracts access_token from response" {
    mock_cmd curl 0 '{"access_token":"abc-123","refresh_token":"r"}'
    VERIFY_CURL="$SYN_MOCK_BIN/curl" run verify::register http://x a@b.com pw Name
    assert_success
    assert_output "abc-123"
}

@test "register: curl failure propagates" {
    mock_cmd curl 22 ''
    VERIFY_CURL="$SYN_MOCK_BIN/curl" run verify::register http://x a@b.com pw Name
    assert_failure
}

@test "create_team: extracts slug" {
    mock_cmd curl 0 '{"slug":"default","name":"Default","id":1}'
    VERIFY_CURL="$SYN_MOCK_BIN/curl" run verify::create_team http://x Default
    assert_success
    assert_output "default"
}

@test "create_project: extracts id" {
    mock_cmd curl 0 '{"id":42,"name":"Demo"}'
    VERIFY_CURL="$SYN_MOCK_BIN/curl" run verify::create_project http://x default Demo
    assert_success
    assert_output "42"
}

@test "create_deployment: extracts name" {
    mock_cmd curl 0 '{"name":"happy-cat-1234","status":"provisioning"}'
    VERIFY_CURL="$SYN_MOCK_BIN/curl" run verify::create_deployment http://x 42 dev
    assert_success
    assert_output "happy-cat-1234"
}

# ---- wait_deployment -----------------------------------------------

@test "wait_deployment: status=running on first poll -> success" {
    mock_cmd curl 0 '{"status":"running"}'
    VERIFY_CURL="$SYN_MOCK_BIN/curl" run verify::wait_deployment http://x happy-cat 5
    assert_success
}

@test "wait_deployment: status=failed -> exit 2" {
    mock_cmd curl 0 '{"status":"failed"}'
    VERIFY_CURL="$SYN_MOCK_BIN/curl" run verify::wait_deployment http://x happy-cat 5
    assert_failure 2
}

@test "wait_deployment: never reaches running -> exit 1" {
    mock_cmd curl 0 '{"status":"provisioning"}'
    VERIFY_CURL="$SYN_MOCK_BIN/curl" run verify::wait_deployment http://x happy-cat 2
    assert_failure 1
}

# ---- check_cli_creds -----------------------------------------------

@test "check_cli_creds: public URL -> echoes URL + success" {
    mock_cmd curl 0 '{"convex_url":"https://synapse.example.com/d/happy-cat"}'
    VERIFY_CURL="$SYN_MOCK_BIN/curl" run verify::check_cli_creds http://x happy-cat
    assert_success
    assert_output "https://synapse.example.com/d/happy-cat"
}

@test "check_cli_creds: 127.0.0.1 URL -> failure (PUBLIC_URL not wired)" {
    mock_cmd curl 0 '{"convex_url":"http://127.0.0.1:3210"}'
    VERIFY_CURL="$SYN_MOCK_BIN/curl" run verify::check_cli_creds http://x happy-cat
    assert_failure 1
    assert_output --partial "loopback"
}

@test "check_cli_creds: localhost URL -> failure" {
    mock_cmd curl 0 '{"convex_url":"http://localhost:3210"}'
    VERIFY_CURL="$SYN_MOCK_BIN/curl" run verify::check_cli_creds http://x happy-cat
    assert_failure 1
}

@test "check_cli_creds: missing field -> failure" {
    mock_cmd curl 0 '{}'
    VERIFY_CURL="$SYN_MOCK_BIN/curl" run verify::check_cli_creds http://x happy-cat
    assert_failure 1
}

# ---- run end-to-end (state-machine mock) ---------------------------

@test "run: full happy path with state-machine curl mock" {
    # Stub curl that branches on the URL it's called with. Each
    # endpoint returns the response the next step expects.
    cat >"$SYN_MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
url=""
for arg in "$@"; do
    case "$arg" in
        http*) url="$arg" ;;
    esac
done
case "$url" in
    *auth/register)       echo '{"access_token":"tok-self","refresh_token":"r"}' ;;
    *teams/create_team)   echo '{"slug":"default","name":"Default"}' ;;
    *create_project)      echo '{"id":1,"name":"Demo"}' ;;
    *create_deployment)   echo '{"name":"happy-cat-self","status":"provisioning"}' ;;
    */deployments/happy-cat-self/cli_credentials)
                          echo '{"convex_url":"https://synapse.example.com/d/happy-cat-self"}' ;;
    */deployments/happy-cat-self)
                          echo '{"status":"running","name":"happy-cat-self"}' ;;
    *)                    echo '{}'; exit 1 ;;
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/curl"
    cat >"$SYN_MOCK_BIN/openssl" <<'EOF'
#!/usr/bin/env bash
[[ "$1 $2" == "rand -hex" ]] && { echo "fixed-pw-fixture"; exit 0; }
exit 1
EOF
    chmod +x "$SYN_MOCK_BIN/openssl"
    VERIFY_CURL="$SYN_MOCK_BIN/curl" \
    VERIFY_OPENSSL="$SYN_MOCK_BIN/openssl" \
    VERIFY_EMAIL="self-test@x" \
        run verify::run http://localhost:8080 --keep-demo
    assert_success
    assert_output --partial "Self-test passed"
}
