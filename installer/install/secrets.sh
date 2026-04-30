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

secrets::gen_jwt()         { "${SECRETS_OPENSSL:-openssl}" rand -hex 64; }
secrets::gen_storage_key() { "${SECRETS_OPENSSL:-openssl}" rand -hex 32; }
secrets::gen_db_password() { "${SECRETS_OPENSSL:-openssl}" rand -hex 16; }

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
        [[ -z "$ph" ]] && continue
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
    local jwt
    jwt="$(secrets::env_get "$file" SYNAPSE_JWT_SECRET)"
    [[ -z "$jwt" ]] && secrets::ensure_env_var "$file" SYNAPSE_JWT_SECRET "$(secrets::gen_jwt)"
    local pwd
    pwd="$(secrets::env_get "$file" POSTGRES_PASSWORD)"
    [[ -z "$pwd" ]] && secrets::ensure_env_var "$file" POSTGRES_PASSWORD "$(secrets::gen_db_password)"
    if (( ha )); then
        local sk
        sk="$(secrets::env_get "$file" SYNAPSE_STORAGE_KEY)"
        [[ -z "$sk" ]] && secrets::ensure_env_var "$file" SYNAPSE_STORAGE_KEY "$(secrets::gen_storage_key)"
    fi
}
