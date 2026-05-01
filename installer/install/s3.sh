# installer/install/s3.sh
# shellcheck shell=bash
#
# S3 upload / download helpers for v1.0+ off-host backups. Uses
# `curl --aws-sigv4` (curl 7.75+) instead of pulling in the aws CLI —
# every supported install host already has curl, and aws CLI on a
# Debian/Ubuntu box is a 50 MB python+pip dance we don't want to own.
#
# S3-compatible providers are first-class: set
# `SYNAPSE_BACKUP_S3_ENDPOINT` to the provider's URL and we use
# path-style addressing (`<endpoint>/<bucket>/<key>`). When the env is
# unset, AWS S3 virtual-hosted style is used
# (`<bucket>.s3.<region>.amazonaws.com/<key>`) — the default for
# anyone not explicitly opting into a different provider.
#
# Credentials follow the AWS conventions:
#   AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY  (required)
#   AWS_REGION or AWS_DEFAULT_REGION  (default us-east-1; "auto" for R2)
#
# Functions:
#   s3::parse_uri    s3://bucket/key/path  → BUCKET=bucket, KEY=key/path
#   s3::resolve_url  build the HTTPS URL for the upload/download target
#   s3::upload       PUT a local file to s3://...
#   s3::download     GET s3://... to a local path
#   s3::check_creds  refuse to call S3 without AWS_ACCESS_KEY_ID + secret
#
# Tests inject S3_CURL=/path/to/mock-curl so the actual HTTPS call
# is mockable without the AWS sigv4 dance.

# ---- creds + URL --------------------------------------------------

# s3::check_creds
# Returns 0 when AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY are both
# set in the env. Returns 2 with a clear message otherwise. Called
# before any upload / download so the operator gets a useful error
# instead of curl returning a 403 from a signed-with-empty-key
# request.
s3::check_creds() {
    if [[ -z "${AWS_ACCESS_KEY_ID:-}" ]]; then
        echo "s3: AWS_ACCESS_KEY_ID is not set in the environment" >&2
        echo "    export it before re-running, or pass via systemd EnvironmentFile" >&2
        return 2
    fi
    if [[ -z "${AWS_SECRET_ACCESS_KEY:-}" ]]; then
        echo "s3: AWS_SECRET_ACCESS_KEY is not set in the environment" >&2
        return 2
    fi
    return 0
}

# s3::parse_uri <s3-uri>
# Splits "s3://bucket/key/path/file.tar.gz" into bucket + key on
# stdout, one per line. Returns 2 on a malformed URI.
#
# Output format:
#   <bucket>
#   <key>
#
# Caller reads with:
#   readarray -t parts < <(s3::parse_uri "$uri") || return 2
#   bucket="${parts[0]}"; key="${parts[1]}"
s3::parse_uri() {
    local uri="${1:-}"
    if [[ "$uri" != s3://* ]]; then
        echo "s3::parse_uri: not an s3:// URI: $uri" >&2
        return 2
    fi
    # Strip the scheme then split on the FIRST slash. Anything after
    # is the key (which may itself contain slashes — S3 keys are flat
    # strings, the slashes are just naming convention).
    local rest="${uri#s3://}"
    local bucket="${rest%%/*}"
    local key="${rest#*/}"
    # If there's no slash at all, key == bucket which is wrong.
    if [[ "$bucket" == "$key" ]]; then
        key=""
    fi
    if [[ -z "$bucket" ]]; then
        echo "s3::parse_uri: empty bucket in $uri" >&2
        return 2
    fi
    printf '%s\n%s\n' "$bucket" "$key"
}

# s3::resolve_url <bucket> <key>
# Returns the full HTTPS URL the upload / download targets. Uses
# `SYNAPSE_BACKUP_S3_ENDPOINT` (path-style) when set; falls back to
# AWS virtual-hosted style otherwise. The region defaults to
# us-east-1 — operators on R2 / Backblaze should set
# AWS_REGION=auto so the sigv4 signature is computed against the
# right region scope.
s3::resolve_url() {
    local bucket="$1" key="$2"
    local endpoint="${SYNAPSE_BACKUP_S3_ENDPOINT:-}"
    local region="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"
    if [[ -n "$endpoint" ]]; then
        # Strip trailing slash so we don't end up with double slashes.
        endpoint="${endpoint%/}"
        printf '%s/%s/%s' "$endpoint" "$bucket" "$key"
    else
        printf 'https://%s.s3.%s.amazonaws.com/%s' "$bucket" "$region" "$key"
    fi
}

# s3::sigv4_provider_string
# Echoes the provider scope curl --aws-sigv4 expects:
# "aws:amz:<region>:s3". Region from AWS_REGION / AWS_DEFAULT_REGION;
# default us-east-1.
s3::sigv4_provider_string() {
    local region="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"
    printf 'aws:amz:%s:s3' "$region"
}

# ---- upload / download ---------------------------------------------

# s3::upload <local_file> <s3_uri>
# PUT the local file at s3://bucket/key. Returns 0 on success, 2 on
# any failure (creds missing / curl exit non-zero / HTTP 4xx-5xx).
# Streams the file body — no in-memory buffering, so multi-GB
# tarballs upload without RAM bloat.
s3::upload() {
    local local_file="$1" uri="$2"
    [[ -f "$local_file" ]] || { echo "s3::upload: $local_file not a file" >&2; return 2; }
    s3::check_creds || return $?

    local parts bucket key
    parts="$(s3::parse_uri "$uri")" || return 2
    # `head -n1` + `tail -n1` look right but break when the parts
    # output is "bucket\n\n" (empty key) — command substitution
    # strips the trailing empty line, leaving just "bucket\n", which
    # tail -n1 returns AS the bucket. sed -n '2p' returns empty when
    # there's no line 2; that's what we want for empty-key cases.
    bucket="$(printf '%s\n' "$parts" | sed -n '1p')"
    key="$(printf '%s\n' "$parts" | sed -n '2p')"
    if [[ -z "$key" ]]; then
        echo "s3::upload: empty key in $uri (must be s3://bucket/key)" >&2
        return 2
    fi

    local url provider
    url="$(s3::resolve_url "$bucket" "$key")"
    provider="$(s3::sigv4_provider_string)"

    local cmd="${S3_CURL:-curl}"
    # --aws-sigv4 needs --user "AWS:SECRET" — curl signs with sigv4
    # using those values. -T uploads via PUT. -sSf: silent on success,
    # print errors, fail on HTTP 4xx-5xx.
    "$cmd" -sSfL \
        --aws-sigv4 "$provider" \
        --user "${AWS_ACCESS_KEY_ID}:${AWS_SECRET_ACCESS_KEY}" \
        -T "$local_file" \
        "$url" || return 2
}

# s3::download <s3_uri> <local_file>
# GET s3://bucket/key into local_file. Returns 0 on success, 2 on
# any failure (creds missing / 404 / 403 / network).
s3::download() {
    local uri="$1" local_file="$2"
    s3::check_creds || return $?

    local parts bucket key
    parts="$(s3::parse_uri "$uri")" || return 2
    # `head -n1` + `tail -n1` look right but break when the parts
    # output is "bucket\n\n" (empty key) — command substitution
    # strips the trailing empty line, leaving just "bucket\n", which
    # tail -n1 returns AS the bucket. sed -n '2p' returns empty when
    # there's no line 2; that's what we want for empty-key cases.
    bucket="$(printf '%s\n' "$parts" | sed -n '1p')"
    key="$(printf '%s\n' "$parts" | sed -n '2p')"
    if [[ -z "$key" ]]; then
        echo "s3::download: empty key in $uri" >&2
        return 2
    fi

    local url provider
    url="$(s3::resolve_url "$bucket" "$key")"
    provider="$(s3::sigv4_provider_string)"

    local cmd="${S3_CURL:-curl}"
    "$cmd" -sSfL \
        --aws-sigv4 "$provider" \
        --user "${AWS_ACCESS_KEY_ID}:${AWS_SECRET_ACCESS_KEY}" \
        -o "$local_file" \
        "$url" || return 2
}

# ---- URI helpers ---------------------------------------------------

# s3::is_s3_uri <string>
# Returns 0 when the argument starts with s3://, 1 otherwise. Lets
# callers branch on "is this a local path or an S3 URI?" without
# parsing the whole thing.
s3::is_s3_uri() {
    [[ "${1:-}" == s3://* ]]
}
