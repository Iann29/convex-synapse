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
# Fixture stub: SECRETS_OPENSSL points at this. Both gen_storage_key
# and gen_updater_token call `rand -hex 32` so they share a fixture
# value — tests that need to distinguish the two assert on the
# env-var key in the rendered file, not on the value.
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

@test "gen_updater_token: returns the openssl stub output (32-byte hex)" {
    # Shares the rand -hex 32 fixture with gen_storage_key — both are
    # 32-byte tokens with no semantic difference at the openssl layer.
    run secrets::gen_updater_token
    assert_success
    assert_output --partial "STORAGEKEY-fixture"
}

@test "gen_updater_token: invokes openssl with rand -hex 32" {
    # Sanity check on the openssl invocation — the spec says 32 bytes
    # (256 bits) of entropy, encoded as 64 hex chars. A typo to -hex 64
    # would still produce a hex string; the only way to assert the
    # right call is to check the argv recorded by the openssl mock.
    cat >"$SYN_MOCK_BIN/openssl" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$@" >>"$SYN_MOCK_CALLS/openssl"
echo "fixture-output"
EOF
    chmod +x "$SYN_MOCK_BIN/openssl"
    run secrets::gen_updater_token
    assert_success
    run cat "$SYN_MOCK_CALLS/openssl"
    assert_line "rand"
    assert_line "-hex"
    assert_line "32"
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

@test "set_env_var: existing non-empty value FORCE-OVERWRITTEN (the whole point)" {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=0.6.0
KEEPME=intact
EOF
    secrets::set_env_var "$ENV_FILE" SYNAPSE_VERSION 0.6.1
    run secrets::env_get "$ENV_FILE" SYNAPSE_VERSION
    assert_output "0.6.1"
    run secrets::env_get "$ENV_FILE" KEEPME
    assert_output "intact"
}

@test "set_env_var: missing key -> appended" {
    cat >"$ENV_FILE" <<EOF
FOO=bar
EOF
    secrets::set_env_var "$ENV_FILE" SYNAPSE_VERSION 0.6.1
    run secrets::env_get "$ENV_FILE" SYNAPSE_VERSION
    assert_output "0.6.1"
    run secrets::env_get "$ENV_FILE" FOO
    assert_output "bar"
}

@test "set_env_var: missing file -> created with mode 0600" {
    local f="$BATS_TEST_TMPDIR/fresh.env"
    secrets::set_env_var "$f" SYNAPSE_VERSION 0.6.1
    [ -f "$f" ]
    run secrets::env_get "$f" SYNAPSE_VERSION
    assert_output "0.6.1"
    local mode
    mode="$(stat -c %a "$f")"
    [[ "$mode" == "600" ]]
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

# ---- ensure_env: SYNAPSE_UPDATER_TOKEN (v1.5.1+) -------------------

@test "ensure_env: generates SYNAPSE_UPDATER_TOKEN when missing" {
    secrets::ensure_env "$ENV_FILE"
    run secrets::env_get "$ENV_FILE" SYNAPSE_UPDATER_TOKEN
    # Stub returns the rand -hex 32 fixture; real installs get a
    # 64-char hex string. Both code paths exercise the same call.
    assert_output --partial "STORAGEKEY-fixture"
}

@test "ensure_env: existing SYNAPSE_UPDATER_TOKEN preserved across re-run" {
    # Idempotency contract: the token rotates only via explicit
    # operator action. A re-run of setup.sh on a working install
    # must NOT change it — every running synapse-api would lose its
    # credential mid-flight.
    cat >"$ENV_FILE" <<EOF
SYNAPSE_UPDATER_TOKEN=existing-updater-token-do-not-touch
EOF
    secrets::ensure_env "$ENV_FILE"
    run secrets::env_get "$ENV_FILE" SYNAPSE_UPDATER_TOKEN
    assert_output "existing-updater-token-do-not-touch"
    # JWT and PG still get generated (regression guard against
    # accidentally short-circuiting the whole function).
    run secrets::env_get "$ENV_FILE" SYNAPSE_JWT_SECRET
    assert_output --partial "JWT-fixture"
    run secrets::env_get "$ENV_FILE" POSTGRES_PASSWORD
    assert_output --partial "PGPASS-fixture"
}

@test "ensure_env: SYNAPSE_UPDATER_TOKEN re-run is idempotent (no duplicates)" {
    secrets::ensure_env "$ENV_FILE"
    secrets::ensure_env "$ENV_FILE"
    secrets::ensure_env "$ENV_FILE"
    run grep -c '^SYNAPSE_UPDATER_TOKEN=' "$ENV_FILE"
    assert_output "1"
}

# ---- ensure_env: SYNAPSE_UPDATER_PORT + SYNAPSE_UPDATER_URL --------
#
# Migration regression: v1.5.0 installs ensured only the TOKEN; the
# upgrade-path smoke on synapse-vps caught that .env was missing the
# PORT and URL keys, so docker-compose's `${SYNAPSE_UPDATER_URL:-}`
# substitution shipped synapse-api with an empty target URL.
# secrets::ensure_env now ensures all three; these tests guard the
# regression.

@test "ensure_env: generates SYNAPSE_UPDATER_PORT default 8089 when missing" {
    secrets::ensure_env "$ENV_FILE"
    run secrets::env_get "$ENV_FILE" SYNAPSE_UPDATER_PORT
    assert_output "8089"
}

@test "ensure_env: generates SYNAPSE_UPDATER_URL default host.docker.internal when missing" {
    secrets::ensure_env "$ENV_FILE"
    run secrets::env_get "$ENV_FILE" SYNAPSE_UPDATER_URL
    assert_output "http://host.docker.internal:8089"
}

@test "ensure_env: existing SYNAPSE_UPDATER_PORT preserved across re-run" {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_UPDATER_PORT=9090
EOF
    secrets::ensure_env "$ENV_FILE"
    run secrets::env_get "$ENV_FILE" SYNAPSE_UPDATER_PORT
    assert_output "9090"
}

@test "ensure_env: existing SYNAPSE_UPDATER_URL preserved across re-run" {
    cat >"$ENV_FILE" <<EOF
SYNAPSE_UPDATER_URL=http://operator-set.example:1234
EOF
    secrets::ensure_env "$ENV_FILE"
    run secrets::env_get "$ENV_FILE" SYNAPSE_UPDATER_URL
    assert_output "http://operator-set.example:1234"
}

@test "ensure_env: SYNAPSE_UPDATER_PORT/URL re-run is idempotent (no duplicates)" {
    secrets::ensure_env "$ENV_FILE"
    secrets::ensure_env "$ENV_FILE"
    secrets::ensure_env "$ENV_FILE"
    run grep -c '^SYNAPSE_UPDATER_PORT=' "$ENV_FILE"
    assert_output "1"
    run grep -c '^SYNAPSE_UPDATER_URL=' "$ENV_FILE"
    assert_output "1"
}

@test "ensure_env: SYNAPSE_UPDATER_URL uses the existing PORT (not the default) when PORT was pre-set" {
    # If an operator pre-set the port to 9999 BEFORE running ensure_env
    # for the first time, the URL should embed 9999 — not the 8089
    # default. Guards against a copy-paste regression where someone
    # hardcodes the URL string and breaks operator-overridden ports.
    cat >"$ENV_FILE" <<EOF
SYNAPSE_UPDATER_PORT=9999
EOF
    secrets::ensure_env "$ENV_FILE"
    run secrets::env_get "$ENV_FILE" SYNAPSE_UPDATER_URL
    assert_output "http://host.docker.internal:9999"
}

@test "ensure_env: v1.5.0 → v1.5.1 migration (token present, port+url missing)" {
    # Real-VPS smoke scenario: a v1.5.0 install had only the TOKEN line
    # because that's all v1.5.0's ensure_env wrote. Upgrading to v1.5.1
    # must add PORT and URL without rotating the token.
    cat >"$ENV_FILE" <<EOF
SYNAPSE_VERSION=1.5.0
SYNAPSE_PORT=8080
SYNAPSE_UPDATER_TOKEN=preserve-me-across-upgrade
EOF
    secrets::ensure_env "$ENV_FILE"
    run secrets::env_get "$ENV_FILE" SYNAPSE_UPDATER_TOKEN
    assert_output "preserve-me-across-upgrade"
    run secrets::env_get "$ENV_FILE" SYNAPSE_UPDATER_PORT
    assert_output "8089"
    run secrets::env_get "$ENV_FILE" SYNAPSE_UPDATER_URL
    assert_output "http://host.docker.internal:8089"
}

# --- v1.5.9 HA backend wiring -------------------------------------------

@test "ensure_env --ha: generates SYNAPSE_BACKEND_POSTGRES_URL pointing at backend-postgres" {
    secrets::ensure_env "$ENV_FILE" --ha
    run secrets::env_get "$ENV_FILE" SYNAPSE_BACKEND_POSTGRES_URL
    [[ "$output" =~ ^postgres://convex:.+@backend-postgres:5432/postgres\?sslmode=disable$ ]] \
        || { echo "got: $output"; return 1; }
}

@test "ensure_env --ha: generates SYNAPSE_BACKEND_S3_* pointing at minio service" {
    secrets::ensure_env "$ENV_FILE" --ha
    run secrets::env_get "$ENV_FILE" SYNAPSE_BACKEND_S3_ENDPOINT
    assert_output "http://minio:9000"
    run secrets::env_get "$ENV_FILE" SYNAPSE_BACKEND_S3_REGION
    assert_output "us-east-1"
    run secrets::env_get "$ENV_FILE" SYNAPSE_BACKEND_S3_BUCKET_PREFIX
    assert_output "convex"

    # Access key + secret are non-empty (random 16/32-byte hex).
    run secrets::env_get "$ENV_FILE" SYNAPSE_BACKEND_S3_ACCESS_KEY
    [ -n "$output" ]
    run secrets::env_get "$ENV_FILE" SYNAPSE_BACKEND_S3_SECRET_KEY
    [ -n "$output" ]
}

@test "ensure_env --ha: HA_PG_USER/PASSWORD seed the bundled cluster-pg" {
    secrets::ensure_env "$ENV_FILE" --ha
    run secrets::env_get "$ENV_FILE" HA_PG_USER
    assert_output "convex"
    run secrets::env_get "$ENV_FILE" HA_PG_PASSWORD
    [ -n "$output" ]
    # The HA_PG_PASSWORD value must equal the one embedded in
    # SYNAPSE_BACKEND_POSTGRES_URL — synapse-api uses the URL, the
    # bundled backend-postgres container uses HA_PG_PASSWORD via
    # docker-compose.yml's POSTGRES_PASSWORD substitution. They MUST
    # match or the connection from synapse-api gets auth-rejected.
    local pwd_in_url
    pwd_in_url="$(secrets::env_get "$ENV_FILE" SYNAPSE_BACKEND_POSTGRES_URL \
        | sed -nE 's|^postgres://convex:([^@]+)@.*|\1|p')"
    assert_equal "$output" "$pwd_in_url"
}

@test "ensure_env --ha: HA_S3_KEY equals SYNAPSE_BACKEND_S3_ACCESS_KEY (minio creds match)" {
    secrets::ensure_env "$ENV_FILE" --ha
    local key_a key_b
    key_a="$(secrets::env_get "$ENV_FILE" HA_S3_KEY)"
    key_b="$(secrets::env_get "$ENV_FILE" SYNAPSE_BACKEND_S3_ACCESS_KEY)"
    assert_equal "$key_a" "$key_b"
}

@test "ensure_env (no --ha): SYNAPSE_BACKEND_* are NOT generated" {
    secrets::ensure_env "$ENV_FILE"
    # The keys may or may not exist in the file (they don't, in fact —
    # ensure_env without --ha skips them entirely). Confirm value is
    # empty either way.
    run secrets::env_get "$ENV_FILE" SYNAPSE_BACKEND_POSTGRES_URL
    assert_output ""
    run secrets::env_get "$ENV_FILE" SYNAPSE_BACKEND_S3_ENDPOINT
    assert_output ""
    run secrets::env_get "$ENV_FILE" HA_PG_PASSWORD
    assert_output ""
}

@test "ensure_env --ha: pre-existing SYNAPSE_BACKEND_POSTGRES_URL preserved (operator BYO)" {
    # Operator with managed Postgres pre-sets the URL before running
    # setup. ensure_env must NOT clobber it with a generated value
    # that points at the bundled cluster-pg.
    cat >"$ENV_FILE" <<EOF2
SYNAPSE_BACKEND_POSTGRES_URL=postgres://prod_user:prod_pass@my-managed-pg.example.com:5432/convex?sslmode=require
EOF2
    secrets::ensure_env "$ENV_FILE" --ha
    run secrets::env_get "$ENV_FILE" SYNAPSE_BACKEND_POSTGRES_URL
    assert_output "postgres://prod_user:prod_pass@my-managed-pg.example.com:5432/convex?sslmode=require"
}

@test "ensure_env --ha: idempotent (re-run preserves credentials)" {
    secrets::ensure_env "$ENV_FILE" --ha
    local pg1 s3k1
    pg1="$(secrets::env_get "$ENV_FILE" HA_PG_PASSWORD)"
    s3k1="$(secrets::env_get "$ENV_FILE" HA_S3_KEY)"

    secrets::ensure_env "$ENV_FILE" --ha
    secrets::ensure_env "$ENV_FILE" --ha
    run secrets::env_get "$ENV_FILE" HA_PG_PASSWORD
    assert_output "$pg1"
    run secrets::env_get "$ENV_FILE" HA_S3_KEY
    assert_output "$s3k1"

    # No duplicate lines either.
    run grep -c '^HA_PG_PASSWORD=' "$ENV_FILE"
    assert_output "1"
    run grep -c '^SYNAPSE_BACKEND_POSTGRES_URL=' "$ENV_FILE"
    assert_output "1"
}
