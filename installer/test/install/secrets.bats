#!/usr/bin/env bats
#
# Unit tests for installer/install/secrets.sh.
#
# Generators are made deterministic by SECRETS_OPENSSL pointing at a
# stub binary that emits a fixed string. The atomic-write + idempotent-
# update helpers operate on $BATS_TEST_TMPDIR fixtures so each test
# starts with a clean slate.

bats_require_minimum_version 1.5.0

load '../helpers/load'

setup() {
    synapse_mock_setup
    # shellcheck source=../../install/secrets.sh
    source "$INSTALLER_DIR/install/secrets.sh"
    ENV_FILE="$BATS_TEST_TMPDIR/.env"
    TMPL_FILE="$INSTALLER_DIR/templates/env.tmpl"
    # Fixed-output openssl stub for deterministic generators.
    cat >"$SYN_MOCK_BIN/openssl" <<'EOF'
#!/usr/bin/env bash
case "$1 $2" in
    "rand -hex")
        case "$3" in
            64) echo "JWT-fixture-jwt-fixture-jwt-fixture-jwt-fixture-jwt-fixture-jwt-fixture-jwt-fixture-jwt-fixture-jwt-fixture-jwt-fixture-jwt-fixt" ;;
            32) echo "STORAGEKEY-fixture-32-bytes-hex-stub-padding!" ;;
            16) echo "PGPASS-fixture16hex" ;;
            *)  echo "UNEXPECTED-LEN-$3" ;;
        esac ;;
    *) echo "UNEXPECTED-CMD-$*" ;;
esac
EOF
    chmod +x "$SYN_MOCK_BIN/openssl"
    SECRETS_OPENSSL="$SYN_MOCK_BIN/openssl"
    export SECRETS_OPENSSL
}

# ---- generators -----------------------------------------------------

@test "gen_jwt: returns the openssl stub output" {
    run secrets::gen_jwt
    assert_success
    assert_output --partial "JWT-fixture-jwt-fixture"
}

@test "gen_storage_key: returns the openssl stub output" {
    run secrets::gen_storage_key
    assert_success
    assert_output --partial "STORAGEKEY-fixture"
}

@test "gen_db_password: returns the openssl stub output" {
    run secrets::gen_db_password
    assert_success
    assert_output --partial "PGPASS-fixture"
}

# ---- env_get --------------------------------------------------------

@test "env_get: missing file -> empty + success" {
    run secrets::env_get /nonexistent/file SYNAPSE_JWT_SECRET
    assert_success
    assert_output ""
}

@test "env_get: KEY=value retrieves bare value" {
    cat >"$ENV_FILE" <<EOF
FOO=bar
SYNAPSE_JWT_SECRET=plain-value-here
EOF
    run secrets::env_get "$ENV_FILE" SYNAPSE_JWT_SECRET
    assert_success
    assert_output "plain-value-here"
}

@test "env_get: KEY=\"quoted\" strips double quotes" {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_JWT_SECRET="quoted-secret"
EOF
    run secrets::env_get "$ENV_FILE" SYNAPSE_JWT_SECRET
    assert_success
    assert_output "quoted-secret"
}

@test "env_get: KEY='single-quoted' strips single quotes" {
    cat >"$ENV_FILE" <<EOF
POSTGRES_PASSWORD='hunter2'
EOF
    run secrets::env_get "$ENV_FILE" POSTGRES_PASSWORD
    assert_success
    assert_output "hunter2"
}

@test "env_get: missing key -> empty" {
    cat >"$ENV_FILE" <<EOF
FOO=bar
EOF
    run secrets::env_get "$ENV_FILE" NOT_THERE
    assert_success
    assert_output ""
}

@test "env_get: empty value (KEY=) -> empty (caller treats as missing)" {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_JWT_SECRET=
EOF
    run secrets::env_get "$ENV_FILE" SYNAPSE_JWT_SECRET
    assert_success
    assert_output ""
}

# ---- ensure_env_var: the idempotency core --------------------------

@test "ensure_env_var: missing file -> created with KEY=value, perms 0600" {
    secrets::ensure_env_var "$ENV_FILE" FOO bar
    [ -f "$ENV_FILE" ]
    run cat "$ENV_FILE"
    assert_output --partial "FOO=bar"
    local mode
    mode="$(stat -c %a "$ENV_FILE")"
    [[ "$mode" == "600" ]]
}

@test "ensure_env_var: empty existing value -> filled" {
    cat >"$ENV_FILE" <<EOF
FOO=
BAR=keep-me
EOF
    secrets::ensure_env_var "$ENV_FILE" FOO new-value
    run secrets::env_get "$ENV_FILE" FOO
    assert_output "new-value"
    run secrets::env_get "$ENV_FILE" BAR
    assert_output "keep-me"
}

@test "ensure_env_var: existing non-empty value PRESERVED (never overwrite)" {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_JWT_SECRET=keep-this-existing-secret
EOF
    secrets::ensure_env_var "$ENV_FILE" SYNAPSE_JWT_SECRET would-clobber
    run secrets::env_get "$ENV_FILE" SYNAPSE_JWT_SECRET
    assert_output "keep-this-existing-secret"
}

@test "ensure_env_var: missing key -> appended" {
    cat >"$ENV_FILE" <<EOF
FOO=bar
EOF
    secrets::ensure_env_var "$ENV_FILE" NEW_KEY new-value
    run secrets::env_get "$ENV_FILE" NEW_KEY
    assert_output "new-value"
    run grep -c '^FOO=' "$ENV_FILE"
    assert_output "1"
}

@test "ensure_env_var: re-run is idempotent (no duplicates)" {
    secrets::ensure_env_var "$ENV_FILE" FOO bar
    secrets::ensure_env_var "$ENV_FILE" FOO bar
    secrets::ensure_env_var "$ENV_FILE" FOO bar
    run grep -c '^FOO=' "$ENV_FILE"
    assert_output "1"
}

# ---- render_env_tmpl ------------------------------------------------

@test "render_env_tmpl: substitutes {{KEY}} from exported vars" {
    local tmpl="$BATS_TEST_TMPDIR/test.tmpl"
    cat >"$tmpl" <<'EOF'
JWT={{JWT}}
PORT={{PORT}}
URL={{URL}}
EOF
    JWT=jwt-val PORT=8080 URL=https://example.com run secrets::render_env_tmpl "$tmpl" "$ENV_FILE"
    assert_success
    run cat "$ENV_FILE"
    assert_line "JWT=jwt-val"
    assert_line "PORT=8080"
    assert_line "URL=https://example.com"
}

@test "render_env_tmpl: refuses to overwrite existing target" {
    local tmpl="$BATS_TEST_TMPDIR/test.tmpl"
    cat >"$tmpl" <<'EOF'
KEY={{VAL}}
EOF
    : >"$ENV_FILE"
    VAL=anything run secrets::render_env_tmpl "$tmpl" "$ENV_FILE"
    assert_failure 1
    assert_output --partial "exists"
}

@test "render_env_tmpl: handles values with slashes and ampersands" {
    local tmpl="$BATS_TEST_TMPDIR/test.tmpl"
    cat >"$tmpl" <<'EOF'
PATH={{P}}
URL={{U}}
EOF
    P="/var/lib/foo" U="https://x?a=1&b=2" run secrets::render_env_tmpl "$tmpl" "$ENV_FILE"
    assert_success
    run cat "$ENV_FILE"
    assert_line "PATH=/var/lib/foo"
    assert_line "URL=https://x?a=1&b=2"
}

@test "render_env_tmpl: missing var -> empty placeholder substitution" {
    local tmpl="$BATS_TEST_TMPDIR/test.tmpl"
    cat >"$tmpl" <<'EOF'
SET={{S}}
UNSET={{NOPE}}
EOF
    S=ok run secrets::render_env_tmpl "$tmpl" "$ENV_FILE"
    assert_success
    run cat "$ENV_FILE"
    assert_line "SET=ok"
    assert_line "UNSET="
}

@test "env.tmpl: header comment must not contain {{PLACEHOLDER}} tokens" {
    # Caught on a real-VPS smoke test: a literal `{{PLACEHOLDERS}}` in
    # the header comment got matched by the renderer and turned into
    # an empty string, leaving "# are filled in by ...". The comment
    # was cosmetic — but if it ever sneaks back, it does the same
    # thing again. Assert the header (everything up to the first
    # `# ---` section divider) has no `{{...}}` tokens.
    local header
    header="$(awk '/^# ---/ { exit } { print }' "$INSTALLER_DIR/templates/env.tmpl")"
    if grep -qE '\{\{[A-Z_][A-Z0-9_]*\}\}' <<<"$header"; then
        echo "header contains a {{...}} placeholder — renderer will eat it" >&2
        echo "--- header ---" >&2
        printf '%s\n' "$header" >&2
        return 1
    fi
}

# ---- ensure_env: end-to-end ----------------------------------------

@test "ensure_env: fresh file gets JWT + PG password generated" {
    secrets::ensure_env "$ENV_FILE"
    run secrets::env_get "$ENV_FILE" SYNAPSE_JWT_SECRET
    assert_output --partial "JWT-fixture"
    run secrets::env_get "$ENV_FILE" POSTGRES_PASSWORD
    assert_output --partial "PGPASS-fixture"
    # No HA -> no storage key
    run secrets::env_get "$ENV_FILE" SYNAPSE_STORAGE_KEY
    assert_output ""
}

@test "ensure_env --ha: also generates storage key" {
    secrets::ensure_env "$ENV_FILE" --ha
    run secrets::env_get "$ENV_FILE" SYNAPSE_STORAGE_KEY
    assert_output --partial "STORAGEKEY-fixture"
}

@test "ensure_env: existing JWT preserved across re-run" {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_JWT_SECRET=existing-jwt-do-not-touch
EOF
    secrets::ensure_env "$ENV_FILE"
    run secrets::env_get "$ENV_FILE" SYNAPSE_JWT_SECRET
    assert_output "existing-jwt-do-not-touch"
    # PG still gets generated
    run secrets::env_get "$ENV_FILE" POSTGRES_PASSWORD
    assert_output --partial "PGPASS-fixture"
}
