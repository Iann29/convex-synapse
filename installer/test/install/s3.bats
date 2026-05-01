#!/usr/bin/env bats
#
# Unit tests for installer/install/s3.sh.
#
# We don't actually upload to S3 — the AWS sigv4 round-trip needs
# real network. We mock `curl` via PATH-shadow and assert the right
# args are passed (URL, --aws-sigv4 provider, --user creds).

bats_require_minimum_version 1.5.0

load '../helpers/load'

setup() {
    synapse_mock_setup
    # shellcheck source=../../install/s3.sh
    source "$INSTALLER_DIR/install/s3.sh"
}

# ---- check_creds ---------------------------------------------------

@test "check_creds: missing AWS_ACCESS_KEY_ID -> exit 2" {
    unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY
    run s3::check_creds
    assert_failure 2
    assert_output --partial "AWS_ACCESS_KEY_ID is not set"
}

@test "check_creds: missing AWS_SECRET_ACCESS_KEY -> exit 2" {
    AWS_ACCESS_KEY_ID=foo
    unset AWS_SECRET_ACCESS_KEY
    run s3::check_creds
    assert_failure 2
    assert_output --partial "AWS_SECRET_ACCESS_KEY"
}

@test "check_creds: both set -> exit 0" {
    AWS_ACCESS_KEY_ID=foo
    AWS_SECRET_ACCESS_KEY=bar
    run s3::check_creds
    assert_success
}

# ---- parse_uri -----------------------------------------------------

@test "parse_uri: bucket + nested key" {
    run s3::parse_uri "s3://my-bucket/a/b/c.tar.gz"
    assert_success
    [[ "${lines[0]}" == "my-bucket" ]] || { echo "line 0: ${lines[0]}" >&2; false; }
    [[ "${lines[1]}" == "a/b/c.tar.gz" ]] || { echo "line 1: ${lines[1]}" >&2; false; }
}

@test "parse_uri: bucket + flat key" {
    run s3::parse_uri "s3://my-bucket/file.tar.gz"
    assert_success
    [[ "${lines[0]}" == "my-bucket" ]]
    [[ "${lines[1]}" == "file.tar.gz" ]]
}

@test "parse_uri: bucket only -> empty key" {
    run s3::parse_uri "s3://my-bucket"
    assert_success
    [[ "${lines[0]}" == "my-bucket" ]]
    [[ "${lines[1]}" == "" ]]
}

@test "parse_uri: not s3:// -> exit 2" {
    run s3::parse_uri "https://example.com/foo"
    assert_failure 2
    assert_output --partial "not an s3:// URI"
}

@test "parse_uri: empty string -> exit 2" {
    run s3::parse_uri ""
    assert_failure 2
}

# ---- resolve_url ---------------------------------------------------

@test "resolve_url: AWS virtual-hosted by default (us-east-1)" {
    unset SYNAPSE_BACKUP_S3_ENDPOINT AWS_REGION AWS_DEFAULT_REGION
    run s3::resolve_url "my-bucket" "a/b/c.tar.gz"
    assert_success
    assert_output "https://my-bucket.s3.us-east-1.amazonaws.com/a/b/c.tar.gz"
}

@test "resolve_url: AWS virtual-hosted respects AWS_REGION" {
    unset SYNAPSE_BACKUP_S3_ENDPOINT
    AWS_REGION=eu-west-2
    run s3::resolve_url "my-bucket" "k.tar.gz"
    assert_success
    assert_output "https://my-bucket.s3.eu-west-2.amazonaws.com/k.tar.gz"
}

@test "resolve_url: SYNAPSE_BACKUP_S3_ENDPOINT switches to path-style" {
    SYNAPSE_BACKUP_S3_ENDPOINT="https://s3.us-west-004.backblazeb2.com"
    run s3::resolve_url "my-bucket" "k.tar.gz"
    assert_success
    assert_output "https://s3.us-west-004.backblazeb2.com/my-bucket/k.tar.gz"
}

@test "resolve_url: trailing slash on endpoint is stripped" {
    SYNAPSE_BACKUP_S3_ENDPOINT="https://endpoint.example.com/"
    run s3::resolve_url "my-bucket" "k.tar.gz"
    assert_success
    assert_output "https://endpoint.example.com/my-bucket/k.tar.gz"
}

# ---- sigv4_provider_string ----------------------------------------

@test "sigv4_provider_string: defaults to us-east-1 when no region set" {
    unset AWS_REGION AWS_DEFAULT_REGION
    run s3::sigv4_provider_string
    assert_success
    assert_output "aws:amz:us-east-1:s3"
}

@test "sigv4_provider_string: respects AWS_REGION" {
    AWS_REGION=auto
    run s3::sigv4_provider_string
    assert_success
    assert_output "aws:amz:auto:s3"
}

# ---- is_s3_uri -----------------------------------------------------

@test "is_s3_uri: matches s3:// prefix" {
    run s3::is_s3_uri "s3://bucket/key"
    assert_success
}

@test "is_s3_uri: rejects local paths" {
    run s3::is_s3_uri "/tmp/backup.tar.gz"
    assert_failure
}

@test "is_s3_uri: rejects https" {
    run s3::is_s3_uri "https://bucket.s3.amazonaws.com/key"
    assert_failure
}

# ---- upload --------------------------------------------------------

@test "upload: missing local file -> exit 2" {
    AWS_ACCESS_KEY_ID=foo AWS_SECRET_ACCESS_KEY=bar
    run s3::upload "/nope/missing.tar.gz" "s3://b/k"
    assert_failure 2
    assert_output --partial "not a file"
}

@test "upload: passes URL + sigv4 + creds to curl" {
    AWS_ACCESS_KEY_ID=AKIA-fake-key
    AWS_SECRET_ACCESS_KEY=secret-fake
    AWS_REGION=us-east-1
    unset SYNAPSE_BACKUP_S3_ENDPOINT

    local local_file="$BATS_TEST_TMPDIR/payload.tar.gz"
    echo "fake archive" >"$local_file"

    cat >"$SYN_MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
echo "$@" >"$BATS_TEST_TMPDIR/curl.args"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/curl"

    S3_CURL="$SYN_MOCK_BIN/curl" \
        run s3::upload "$local_file" "s3://my-bucket/path/to/archive.tar.gz"
    assert_success
    run cat "$BATS_TEST_TMPDIR/curl.args"
    assert_output --partial "--aws-sigv4 aws:amz:us-east-1:s3"
    assert_output --partial "--user AKIA-fake-key:secret-fake"
    assert_output --partial "https://my-bucket.s3.us-east-1.amazonaws.com/path/to/archive.tar.gz"
    # -T uploads via PUT
    assert_output --partial "-T $local_file"
}

@test "upload: surfaces curl failure as exit 2" {
    AWS_ACCESS_KEY_ID=k AWS_SECRET_ACCESS_KEY=s
    local local_file="$BATS_TEST_TMPDIR/payload.tar.gz"
    : >"$local_file"
    cat >"$SYN_MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
exit 22
EOF
    chmod +x "$SYN_MOCK_BIN/curl"
    S3_CURL="$SYN_MOCK_BIN/curl" \
        run s3::upload "$local_file" "s3://my-bucket/key.tar.gz"
    assert_failure 2
}

@test "upload: empty key in URI -> exit 2" {
    AWS_ACCESS_KEY_ID=k AWS_SECRET_ACCESS_KEY=s
    local local_file="$BATS_TEST_TMPDIR/payload.tar.gz"
    : >"$local_file"
    run s3::upload "$local_file" "s3://my-bucket"
    assert_failure 2
    assert_output --partial "empty key"
}

# ---- download ------------------------------------------------------

@test "download: passes URL + sigv4 + creds to curl + output path" {
    AWS_ACCESS_KEY_ID=k AWS_SECRET_ACCESS_KEY=s
    AWS_REGION=eu-west-1
    unset SYNAPSE_BACKUP_S3_ENDPOINT
    cat >"$SYN_MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
echo "$@" >"$BATS_TEST_TMPDIR/curl.args"
# Pretend the download succeeded by writing whatever was after -o
out=""
prev=""
for a in "$@"; do
    if [[ "$prev" == "-o" ]]; then out="$a"; break; fi
    prev="$a"
done
[[ -n "$out" ]] && echo "fake-body" >"$out"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/curl"
    local out="$BATS_TEST_TMPDIR/got.tar.gz"
    S3_CURL="$SYN_MOCK_BIN/curl" \
        run s3::download "s3://my-bucket/k/v.tar.gz" "$out"
    assert_success
    [ -f "$out" ]
    run cat "$BATS_TEST_TMPDIR/curl.args"
    assert_output --partial "--aws-sigv4 aws:amz:eu-west-1:s3"
    assert_output --partial "--user k:s"
    assert_output --partial "https://my-bucket.s3.eu-west-1.amazonaws.com/k/v.tar.gz"
    assert_output --partial "-o $out"
}

@test "download: SYNAPSE_BACKUP_S3_ENDPOINT routes via path-style" {
    AWS_ACCESS_KEY_ID=k AWS_SECRET_ACCESS_KEY=s
    SYNAPSE_BACKUP_S3_ENDPOINT="https://b2.example.com"
    cat >"$SYN_MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
echo "$@" >"$BATS_TEST_TMPDIR/curl.args"
out=""
prev=""
for a in "$@"; do
    if [[ "$prev" == "-o" ]]; then out="$a"; break; fi
    prev="$a"
done
[[ -n "$out" ]] && echo "x" >"$out"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/curl"
    S3_CURL="$SYN_MOCK_BIN/curl" \
        run s3::download "s3://my-bucket/key" "$BATS_TEST_TMPDIR/out"
    assert_success
    run cat "$BATS_TEST_TMPDIR/curl.args"
    assert_output --partial "https://b2.example.com/my-bucket/key"
}
