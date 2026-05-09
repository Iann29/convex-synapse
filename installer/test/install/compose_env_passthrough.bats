#!/usr/bin/env bats
#
# Regression for the v1.5.2 KVM4 SYNAPSE_STORAGE_KEY bug. Every
# SYNAPSE_* env var the Go binary reads via os.Getenv MUST also be
# in the synapse-api service's environment block in
# docker-compose.yml. Otherwise the var lives in .env, the operator
# thinks it's plumbed through, and the container silently runs with
# defaults — surfacing as nil-pointer panics or "feature appears
# wired but doesn't work" deep in the field.
#
# The bug was caught on a fresh KVM4 install where SYNAPSE_STORAGE_KEY
# was generated in .env by the installer but never reached the
# container, so crypto.NewFromEnv() returned an error, secretBox
# stayed nil, and /v1/admin/dns_credentials/cloudflare panicked on
# EncryptString.
#
# Coverage approach: scrape every os.Getenv("SYNAPSE_*") + os.LookupEnv
# call in the synapse Go module, scrape every SYNAPSE_* key in the
# synapse-api service's environment: block of docker-compose.yml,
# diff the sets. Any Go-side var missing from compose fails the test
# with the offender named.
#
# Allowlist: a few SYNAPSE_* vars the binary reads OPTIONALLY for
# legacy / test-only purposes. New entries here need a justification
# comment.

bats_require_minimum_version 1.5.0

setup() {
    REPO_ROOT="${BATS_TEST_DIRNAME}/../../../"
    if [[ ! -d "$REPO_ROOT/synapse" ]]; then
        skip "synapse Go module not present in expected layout"
    fi
}

@test "compose: every SYNAPSE_* env Go reads is also in docker-compose.yml synapse-api environment" {
    # Vars the installer / Go binary reference but compose intentionally
    # doesn't expose to the synapse-api container. Justification per
    # entry — keep this list short.
    declare -A allowlist=(
        # Operator-set on the host shell, never read inside the
        # container. The container's setup.sh is a different beast.
        ["SYNAPSE_INSTALL_DIR"]="installer-host-only"
        ["SYNAPSE_INSTALL_LOG"]="installer-host-only"
        ["SYNAPSE_BOOTSTRAP_REPO_URL"]="installer-host-only"
        ["SYNAPSE_BOOTSTRAP_REF"]="installer-host-only"
        ["SYNAPSE_VERSION"]="render-time-only (build arg, not runtime env)"
        ["SYNAPSE_CADDYFILE_PATH"]="installer-host-only"
        ["SYNAPSE_CADDYFILE_BACKUP"]="installer-host-only"
        ["SYNAPSE_HA_E2E"]="gated test-only env"
        ["SYNAPSE_DASHBOARD_UPSTREAM"]="dashboard-side, not synapse-api"
        ["SYNAPSE_TEST_DB_URL"]="test harness, not container runtime"
        ["SYNAPSE_BACKUP_S3_ENDPOINT"]="installer/backup-flow only"
        ["SYNAPSE_UPDATER_BIND"]="daemon-side env, not synapse-api"
        ["SYNAPSE_UPDATER_PORT"]="daemon-side env (synapse-api uses URL+TOKEN)"
        ["SYNAPSE_UPDATER_NO_RESTART"]="installer-side guard, not synapse-api"
        ["SYNAPSE_UPDATER_STATE_DIR"]="daemon-side state path"
        ["SYNAPSE_UPDATER_LOG_DIR"]="daemon-side log path"
        ["SYNAPSE_POSTGRES_CONTAINER"]="daemon-side docker exec target"
    )

    # Scrape Go side. Match `os.Getenv("SYNAPSE_…")` and
    # `os.LookupEnv("SYNAPSE_…")` and `getEnvDefault("SYNAPSE_…", …)`.
    local go_vars
    go_vars="$(grep -rhoE '"(SYNAPSE_[A-Z0-9_]+)"' "$REPO_ROOT/synapse/" \
        --include='*.go' \
        | tr -d '"' \
        | sort -u)"

    # Scrape compose synapse-api env block. Limit to the synapse-api
    # service so we don't false-positive on dashboard/postgres entries.
    local compose_block
    compose_block="$(awk '
        /^  synapse:/{found=1; next}
        found && /^  [a-z]/{exit}
        found{print}
    ' "$REPO_ROOT/docker-compose.yml")"

    local compose_vars
    compose_vars="$(echo "$compose_block" \
        | grep -oE '^      SYNAPSE_[A-Z0-9_]+:' \
        | tr -d ': ' \
        | sort -u)"

    local missing=""
    for var in $go_vars; do
        # Skip allowlist entries.
        if [[ -n "${allowlist[$var]:-}" ]]; then
            continue
        fi
        if ! echo "$compose_vars" | grep -qx "$var"; then
            missing+="$var"$'\n'
        fi
    done

    if [[ -n "$missing" ]]; then
        printf 'Go-side env vars NOT exposed in docker-compose.yml synapse-api environment:\n%s\n' "$missing"
        printf 'Add each to the synapse service environment block, OR add to this test allowlist with a justification comment.\n'
        false
    fi
}
