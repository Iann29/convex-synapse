# installer/install/secrets.sh
# shellcheck shell=bash
#
# Secret generation + .env rendering. Strict idempotency contract:
# re-running setup.sh on a working install MUST NOT regenerate any
# secret that's already in place. A new JWT secret would invalidate
# every active session; a new SYNAPSE_STORAGE_KEY would orphan every
# encrypted blob in deployment_storage. The Coolify `update_env_var`
# pattern (only fill empty KEY= / append if missing) is the canonical
# way to express this in shell.
#
# Functions:
#   secrets::gen_jwt          → 64-byte hex (openssl rand -hex 64)
#   secrets::gen_storage_key  → 32-byte hex
#   secrets::gen_db_password  → 16-byte hex
#   secrets::render_env_tmpl  → render template into a fresh env file
#   secrets::ensure_env_var   → idempotent KEY=VAL update
#   secrets::ensure_env       → end-to-end "make sure .env has every
#                               secret" entry point
#
# Tests inject SECRETS_OPENSSL=/path/to/fixture-openssl to make the
# "generate" calls deterministic.

# ---- pure generators ------------------------------------------------

secrets::gen_jwt()           { "${SECRETS_OPENSSL:-openssl}" rand -hex 64; }
secrets::gen_storage_key()   { "${SECRETS_OPENSSL:-openssl}" rand -hex 32; }
secrets::gen_db_password()   { "${SECRETS_OPENSSL:-openssl}" rand -hex 16; }
secrets::gen_updater_token() { "${SECRETS_OPENSSL:-openssl}" rand -hex 32; }

# secrets::detect_public_ip → echo the host's public IPv4, or empty.
#
# Order:
#   1. If $SYNAPSE_PUBLIC_IP is exported (operator override, CI flag),
#      echo that — we trust the caller.
#   2. If $SYNAPSE_DETECTED_PUBLIC_IP is set (wizard.sh stamps this
#      during the interactive install), reuse it instead of probing
#      ipify a second time.
#   3. Live probe via api.ipify.org with a 5s timeout. The endpoint
#      returns the bare IPv4 in the body — we validate the shape with
#      a tiny IPv4 regex so a Cloudflare error page doesn't get
#      mistaken for an address.
#
# Always returns 0; the caller checks for empty output. Override the
# probe URL via $SECRETS_IPIFY_URL for tests.
secrets::detect_public_ip() {
    if [[ -n "${SYNAPSE_PUBLIC_IP:-}" ]]; then
        printf '%s\n' "$SYNAPSE_PUBLIC_IP"
        return 0
    fi
    if [[ -n "${SYNAPSE_DETECTED_PUBLIC_IP:-}" ]]; then
        printf '%s\n' "$SYNAPSE_DETECTED_PUBLIC_IP"
        return 0
    fi
    local probe_url="${SECRETS_IPIFY_URL:-https://api.ipify.org}"
    local probe
    probe="$("${SECRETS_CURL:-curl}" -sf --max-time 5 "$probe_url" 2>/dev/null || true)"
    # Trim whitespace + a trailing newline if the endpoint added one.
    probe="${probe//[[:space:]]/}"
    # IPv4 sanity check: 1-3 digit octets with three dots. We don't
    # care about value-range correctness here — pgx / docker will
    # reject "999.999.999.999" later if the probe ever returned junk.
    if [[ "$probe" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]; then
        printf '%s\n' "$probe"
        return 0
    fi
    return 0
}

# ---- atomic file write ---------------------------------------------

# secrets::_write_atomic <dst>
# Reads stdin into a tempfile next to <dst>, then renames into place
# (POSIX atomic on the same filesystem). The pre-existing perm bits
# are preserved so subsequent renders don't accidentally widen access
# on a chmod 600 .env.
secrets::_write_atomic() {
    local dst="$1"
    local tmp
    tmp="$(mktemp "${dst}.XXXXXX")" || return 2
    cat >"$tmp"
    if [[ -e "$dst" ]]; then
        # `chmod --reference` is GNU. Fall back to read-then-set for
        # portability across the BSD-coreutils corner. We only target
        # Linux right now so the fallback should never trigger, but
        # the cost of guarding is one line.
        chmod --reference="$dst" "$tmp" 2>/dev/null \
            || chmod 0600 "$tmp"
    else
        chmod 0600 "$tmp"
    fi
    mv -f "$tmp" "$dst"
}

# ---- env-file accessors --------------------------------------------

# secrets::env_get <env_file> <key> → echo value or empty.
# Strips surrounding quotes (single or double) but not embedded
# whitespace. Only matches lines that actually look like KEY=VALUE
# (no comments, no blank lines).
secrets::env_get() {
    local file="$1" key="$2"
    [[ -r "$file" ]] || return 0
    local line val
    line="$(grep -E "^${key}=" "$file" | tail -n1)" || return 0
    val="${line#"${key}"=}"
    # Strip "..." or '...' wrappers — operators occasionally quote the
    # value when copy-pasting from a doc.
    val="${val#\"}"; val="${val%\"}"
    val="${val#\'}"; val="${val%\'}"
    printf '%s' "$val"
}

# secrets::ensure_env_var <env_file> <key> <value>
# If KEY is missing OR set to empty in env_file, fill it with $value.
# Existing non-empty values are preserved (the whole point of the
# helper). The function is idempotent and safe to call from a
# re-running setup.sh.
secrets::ensure_env_var() {
    local file="$1" key="$2" value="$3"
    [[ -f "$file" ]] || { : >"$file"; chmod 0600 "$file"; }
    local existing
    existing="$(secrets::env_get "$file" "$key")"
    if [[ -n "$existing" ]]; then
        return 0
    fi
    local tmp
    tmp="$(mktemp "${file}.XXXXXX")" || return 2
    if grep -qE "^${key}=" "$file"; then
        # Empty existing → replace in place.
        awk -v k="$key" -v v="$value" '
            BEGIN { FS = "=" }
            $1 == k { print k "=" v; next }
            { print }
        ' "$file" >"$tmp"
    else
        # Missing → append.
        cat "$file" >"$tmp"
        printf '%s=%s\n' "$key" "$value" >>"$tmp"
    fi
    chmod --reference="$file" "$tmp" 2>/dev/null || chmod 0600 "$tmp"
    mv -f "$tmp" "$file"
}

# secrets::set_env_var <env_file> <key> <value>
# Force-overwrite KEY=value. Same in-place semantics as ensure_env_var
# but does NOT preserve an existing non-empty value — used for stamps
# (SYNAPSE_VERSION) that legitimately change on every upgrade. Append
# the line if the key is missing entirely.
#
# Note: the strict idempotency contract on the secret-generators is
# preserved because nobody calls set_env_var on JWT/PG/storage keys.
# Reach for this helper only when you actually want a fresh value.
secrets::set_env_var() {
    local file="$1" key="$2" value="$3"
    [[ -f "$file" ]] || { : >"$file"; chmod 0600 "$file"; }
    local tmp
    tmp="$(mktemp "${file}.XXXXXX")" || return 2
    if grep -qE "^${key}=" "$file"; then
        awk -v k="$key" -v v="$value" '
            BEGIN { FS = "=" }
            $1 == k { print k "=" v; next }
            { print }
        ' "$file" >"$tmp"
    else
        cat "$file" >"$tmp"
        printf '%s=%s\n' "$key" "$value" >>"$tmp"
    fi
    chmod --reference="$file" "$tmp" 2>/dev/null || chmod 0600 "$tmp"
    mv -f "$tmp" "$file"
}

# secrets::render_env_tmpl <template> <out>
# Substitutes {{KEY}} placeholders in <template> with the values of
# the same-named exported env vars, writes the result to <out>
# atomically. Used ONLY when <out> doesn't exist yet — for re-runs,
# secrets::ensure_env / secrets::ensure_env_var preserve existing
# values. Refuses to overwrite an existing target so a misconfigured
# call can't wipe out an operator's working .env.
secrets::render_env_tmpl() {
    local tmpl="$1" out="$2"
    [[ -r "$tmpl" ]] || { echo "secrets::render_env_tmpl: $tmpl unreadable" >&2; return 2; }
    if [[ -e "$out" ]]; then
        echo "secrets::render_env_tmpl: $out exists; refusing to overwrite" >&2
        return 1
    fi
    # envsubst is the canonical tool, but it needs the var list to be
    # restricted (otherwise it expands ANY $-reference in the file,
    # turning a stray "$PORT" comment into garbage). We do explicit
    # {{KEY}} substitution via sed instead so the template stays
    # bash-syntax-agnostic and the substitution rules are obvious.
    local tmp
    tmp="$(mktemp "${out}.XXXXXX")" || return 2
    cp "$tmpl" "$tmp"
    local placeholders
    placeholders="$(grep -oE '\{\{[A-Z_][A-Z0-9_]*\}\}' "$tmpl" | sort -u)"
    local ph key val
    while IFS= read -r ph; do
        # `[[ ]] && cmd` pattern is a set -e footgun (returns the
        # test's exit code when false, aborting the loop). Use the
        # explicit form everywhere.
        if [[ -z "$ph" ]]; then continue; fi
        key="${ph#\{\{}"; key="${key%\}\}}"
        val="${!key:-}"
        # sed-escape val: backslashes, ampersands, and the chosen
        # delimiter (|). Operators sometimes set values with slashes
        # (URLs, file paths), so | is a safer delimiter than /.
        local esc
        esc="$(printf '%s' "$val" | sed -e 's/[\&|]/\\&/g')"
        sed -i.bak "s|${ph}|${esc}|g" "$tmp" && rm -f "${tmp}.bak"
    done <<<"$placeholders"
    chmod 0600 "$tmp"
    mv -f "$tmp" "$out"
}

# secrets::ensure_env <env_file> [--ha]
# End-to-end "make sure $env_file has every secret a healthy install
# needs". Generates only the values that are missing, preserving
# anything the operator (or a previous run) put there.
secrets::ensure_env() {
    local file="$1" ha=0
    shift
    while (( $# > 0 )); do
        case "$1" in
            --ha) ha=1 ;;
        esac
        shift
    done
    # `[[ -z "$x" ]] && cmd` returns 1 (the test's exit code) when
    # $x is non-empty, which under set -e aborts the function. Use
    # explicit `if`/`fi` for any conditional whose downstream caller
    # has set -e enabled (every consumer of these helpers does).
    local jwt
    jwt="$(secrets::env_get "$file" SYNAPSE_JWT_SECRET)"
    if [[ -z "$jwt" ]]; then
        secrets::ensure_env_var "$file" SYNAPSE_JWT_SECRET "$(secrets::gen_jwt)"
    fi
    local pwd
    pwd="$(secrets::env_get "$file" POSTGRES_PASSWORD)"
    if [[ -z "$pwd" ]]; then
        secrets::ensure_env_var "$file" POSTGRES_PASSWORD "$(secrets::gen_db_password)"
    fi
    # Self-update daemon bearer token (v1.5.1+). Same idempotency
    # contract as JWT/POSTGRES_PASSWORD: generate once, preserve on
    # every re-render. Rotating it would invalidate the credential
    # baked into every running synapse-api container until the next
    # `docker compose up -d`, so it stays sticky.
    local utok
    utok="$(secrets::env_get "$file" SYNAPSE_UPDATER_TOKEN)"
    if [[ -z "$utok" ]]; then
        secrets::ensure_env_var "$file" SYNAPSE_UPDATER_TOKEN "$(secrets::gen_updater_token)"
    fi
    # SYNAPSE_UPDATER_PORT + SYNAPSE_UPDATER_URL also need to be in
    # .env for upgrade-path migrations from v1.5.0 (which had only the
    # token) and any other state where the template rendered before
    # these keys existed. Without them, docker-compose's
    # `${SYNAPSE_UPDATER_URL:-}` substitution leaves synapse-api with
    # an empty target URL and every "Check for updates" call fails
    # silently. ensure_env_var no-ops when the key is already set, so
    # operators who exported a custom port/url upstream are preserved.
    local uport
    uport="$(secrets::env_get "$file" SYNAPSE_UPDATER_PORT)"
    if [[ -z "$uport" ]]; then
        secrets::ensure_env_var "$file" SYNAPSE_UPDATER_PORT "${SYNAPSE_UPDATER_PORT:-8089}"
        uport="${SYNAPSE_UPDATER_PORT:-8089}"
    fi
    local uurl
    uurl="$(secrets::env_get "$file" SYNAPSE_UPDATER_URL)"
    if [[ -z "$uurl" ]]; then
        secrets::ensure_env_var "$file" SYNAPSE_UPDATER_URL \
            "${SYNAPSE_UPDATER_URL:-http://host.docker.internal:${uport}}"
    fi
    # SYNAPSE_PUBLIC_IP (v1.6.6+). Required for the per-deployment
    # custom-domain DNS verification + auto-config flow: without it,
    # added domains silently stay 'pending' and the dashboard shows
    # the "DNS verification disabled" banner. Pre-1.6.6 the wizard
    # detected the IP for the summary screen but never wrote it to
    # .env, so every install since v1.0 has been arriving here with
    # the var unset. Top-up + auto-detect heals existing installs on
    # the next setup.sh --upgrade (driven by the dashboard "update"
    # button); fresh installs get it on first phase_secrets.
    #
    # Detection order (first non-empty wins):
    #   1. operator-exported $SYNAPSE_PUBLIC_IP (e.g. via flag in CI)
    #   2. live external probe (api.ipify.org, 5s timeout)
    # Both can fail (offline VPS, ipify unreachable); on a miss we
    # log + continue without writing a stub value — the operator
    # then knows to set it manually.
    local pub_ip
    pub_ip="$(secrets::env_get "$file" SYNAPSE_PUBLIC_IP)"
    if [[ -z "$pub_ip" ]]; then
        local detected
        detected="$(secrets::detect_public_ip)" || detected=""
        if [[ -n "$detected" ]]; then
            secrets::ensure_env_var "$file" SYNAPSE_PUBLIC_IP "$detected"
        fi
    fi
    if (( ha )); then
        local sk
        sk="$(secrets::env_get "$file" SYNAPSE_STORAGE_KEY)"
        if [[ -z "$sk" ]]; then
            secrets::ensure_env_var "$file" SYNAPSE_STORAGE_KEY "$(secrets::gen_storage_key)"
        fi
        # v1.5.9 backend credentials. Pre-1.5.9 ENABLE_HA installs
        # only stamped SYNAPSE_HA_ENABLED + SYNAPSE_STORAGE_KEY but
        # never the SYNAPSE_BACKEND_* URLs that synapse-api needs to
        # talk to the bundled cluster-pg + minio. Top-up here so
        # upgrades from broken HA installs heal automatically. We
        # generate fresh credentials only when missing — operators
        # who pre-set their own managed Postgres+S3 are preserved.
        local ha_pg
        ha_pg="$(secrets::env_get "$file" HA_PG_PASSWORD)"
        if [[ -z "$ha_pg" ]]; then
            ha_pg="$(secrets::gen_db_password)"
            secrets::ensure_env_var "$file" HA_PG_USER "convex"
            secrets::ensure_env_var "$file" HA_PG_PASSWORD "$ha_pg"
        fi
        local ha_s3_key
        ha_s3_key="$(secrets::env_get "$file" HA_S3_KEY)"
        if [[ -z "$ha_s3_key" ]]; then
            ha_s3_key="$(secrets::gen_db_password)"
            secrets::ensure_env_var "$file" HA_S3_KEY "$ha_s3_key"
        fi
        local ha_s3_secret
        ha_s3_secret="$(secrets::env_get "$file" HA_S3_SECRET)"
        if [[ -z "$ha_s3_secret" ]]; then
            ha_s3_secret="$(secrets::gen_storage_key)"
            secrets::ensure_env_var "$file" HA_S3_SECRET "$ha_s3_secret"
        fi
        local backend_pg
        backend_pg="$(secrets::env_get "$file" SYNAPSE_BACKEND_POSTGRES_URL)"
        if [[ -z "$backend_pg" ]]; then
            secrets::ensure_env_var "$file" SYNAPSE_BACKEND_POSTGRES_URL \
                "postgres://convex:${ha_pg}@backend-postgres:5432/postgres?sslmode=disable"
        fi
        local backend_s3_ep
        backend_s3_ep="$(secrets::env_get "$file" SYNAPSE_BACKEND_S3_ENDPOINT)"
        if [[ -z "$backend_s3_ep" ]]; then
            secrets::ensure_env_var "$file" SYNAPSE_BACKEND_S3_ENDPOINT "http://minio:9000"
        fi
        local backend_s3_region
        backend_s3_region="$(secrets::env_get "$file" SYNAPSE_BACKEND_S3_REGION)"
        if [[ -z "$backend_s3_region" ]]; then
            secrets::ensure_env_var "$file" SYNAPSE_BACKEND_S3_REGION "us-east-1"
        fi
        local backend_s3_access
        backend_s3_access="$(secrets::env_get "$file" SYNAPSE_BACKEND_S3_ACCESS_KEY)"
        if [[ -z "$backend_s3_access" ]]; then
            secrets::ensure_env_var "$file" SYNAPSE_BACKEND_S3_ACCESS_KEY "$ha_s3_key"
        fi
        local backend_s3_secret
        backend_s3_secret="$(secrets::env_get "$file" SYNAPSE_BACKEND_S3_SECRET_KEY)"
        if [[ -z "$backend_s3_secret" ]]; then
            secrets::ensure_env_var "$file" SYNAPSE_BACKEND_S3_SECRET_KEY "$ha_s3_secret"
        fi
        local backend_s3_bucket
        backend_s3_bucket="$(secrets::env_get "$file" SYNAPSE_BACKEND_S3_BUCKET_PREFIX)"
        if [[ -z "$backend_s3_bucket" ]]; then
            secrets::ensure_env_var "$file" SYNAPSE_BACKEND_S3_BUCKET_PREFIX "convex"
        fi
    fi
}
