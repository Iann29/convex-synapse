# installer/install/verify.sh
# shellcheck shell=bash
#
# Post-install self-test. Runs through the entire happy path of the
# Synapse public API (register → team → project → deployment → CLI
# credentials) so the operator sees green checks for every layer
# before being handed off to the dashboard.
#
# Hard requirement: jq + curl. We don't fall back to grep/sed JSON
# parsing — that bug class isn't worth it. preflight should have
# nudged the operator to install both.
#
# Tests inject VERIFY_CURL=/path/to/stub-curl to drive deterministic
# responses without spinning up a real Synapse stack.

# verify::_curl <method> <url> [json_body] → stdout: response body.
# Internal wrapper. Exits non-zero on HTTP error (curl -f), so the
# caller can use `if`/`||`. Uses Bearer auth automatically when
# VERIFY_TOKEN is set in the env.
verify::_curl() {
    local method="$1" url="$2" body="${3:-}"
    local cmd="${VERIFY_CURL:-curl}"
    local args=(-sf -X "$method" -H 'Content-Type: application/json')
    if [[ -n "${VERIFY_TOKEN:-}" ]]; then
        args+=(-H "Authorization: Bearer $VERIFY_TOKEN")
    fi
    if [[ -n "$body" ]]; then
        args+=(-d "$body")
    fi
    args+=("$url")
    "$cmd" "${args[@]}"
}

# verify::_jq <expression> → wraps jq with a stable command override.
verify::_jq() {
    local cmd="${VERIFY_JQ:-jq}"
    "$cmd" -r "$1"
}

# verify::register <synapse_url> <email> <password> <name> → token on stdout
verify::register() {
    local url="$1" email="$2" password="$3" name="$4"
    local body resp
    body="$(printf '{"email":"%s","password":"%s","name":"%s"}' "$email" "$password" "$name")"
    resp="$(verify::_curl POST "$url/v1/auth/register" "$body")" || return 1
    printf '%s' "$resp" | verify::_jq '.access_token'
}

# verify::create_team <synapse_url> <name> → team_ref on stdout
verify::create_team() {
    local url="$1" name="$2"
    local body resp
    body="$(printf '{"name":"%s"}' "$name")"
    resp="$(verify::_curl POST "$url/v1/teams/create_team" "$body")" || return 1
    printf '%s' "$resp" | verify::_jq '.slug // .team.slug // .reference'
}

# verify::create_project <synapse_url> <team_ref> <name> → project_id on stdout
verify::create_project() {
    local url="$1" team="$2" name="$3"
    local body resp
    body="$(printf '{"name":"%s"}' "$name")"
    resp="$(verify::_curl POST "$url/v1/teams/$team/create_project" "$body")" || return 1
    printf '%s' "$resp" | verify::_jq '.id // .project.id'
}

# verify::create_deployment <synapse_url> <project_id> <type> → deployment_name
verify::create_deployment() {
    local url="$1" pid="$2" dtype="${3:-dev}"
    local body resp
    body="$(printf '{"deployment_type":"%s"}' "$dtype")"
    resp="$(verify::_curl POST "$url/v1/projects/$pid/create_deployment" "$body")" || return 1
    printf '%s' "$resp" | verify::_jq '.name // .deployment.name'
}

# verify::wait_deployment <synapse_url> <deployment_name> [timeout=120]
# Polls until status=running OR timeout. Returns 0/1.
verify::wait_deployment() {
    local url="$1" name="$2" timeout="${3:-120}"
    local elapsed=0 status=""
    local resp
    while (( elapsed < timeout )); do
        resp="$(verify::_curl GET "$url/v1/deployments/$name" 2>/dev/null)" || true
        status="$(printf '%s' "$resp" | verify::_jq '.status // empty' 2>/dev/null)"
        case "$status" in
            running) return 0 ;;
            failed)  return 2 ;;
        esac
        sleep 2
        elapsed=$(( elapsed + 2 ))
    done
    return 1
}

# verify::check_cli_creds <synapse_url> <deployment_name> → URL on stdout
# Asserts the CLI URL is NOT 127.0.0.1 (which would mean
# SYNAPSE_PUBLIC_URL wasn't wired up — the whole point of v0.6's
# install flow). Returns 1 if the URL still references loopback.
verify::check_cli_creds() {
    local url="$1" name="$2"
    local resp convex_url
    resp="$(verify::_curl GET "$url/v1/deployments/$name/cli_credentials")" || return 1
    convex_url="$(printf '%s' "$resp" | verify::_jq '.convex_url // .ConvexURL // empty')"
    if [[ -z "$convex_url" ]]; then
        return 1
    fi
    case "$convex_url" in
        *127.0.0.1*|*localhost*)
            echo "verify::check_cli_creds: CLI URL still references loopback ($convex_url)" >&2
            return 1
            ;;
    esac
    printf '%s' "$convex_url"
}

# verify::run <synapse_url> [--keep-demo]
# Full happy-path self-test. Registers a one-shot admin, creates a
# team, project, deployment, waits for running, validates the CLI
# credentials URL is publicly reachable. Tears the demo down at the
# end unless --keep-demo. Returns 0 on success.
verify::run() {
    local url="$1"
    shift || true
    local keep=0
    while (( $# > 0 )); do
        case "$1" in
            --keep-demo) keep=1 ;;
        esac
        shift
    done
    local email password token team pid dep convex_url
    email="${VERIFY_EMAIL:-admin-$(date +%s)@synapse.local}"
    password="${VERIFY_PASSWORD:-$("${VERIFY_OPENSSL:-openssl}" rand -hex 16)}"

    ui::info "Self-test: registering one-shot admin"
    token="$(verify::register "$url" "$email" "$password" "Self-test admin")" || {
        ui::fail "Self-test: register failed"
        return 2
    }
    [[ -z "$token" || "$token" == "null" ]] && { ui::fail "Self-test: no access_token"; return 2; }
    export VERIFY_TOKEN="$token"

    ui::info "Self-test: creating Default team + Demo project + dev deployment"
    team="$(verify::create_team "$url" "Default")" || { ui::fail "Self-test: team create failed"; return 2; }
    pid="$(verify::create_project "$url" "$team" "Demo")" || { ui::fail "Self-test: project create failed"; return 2; }
    dep="$(verify::create_deployment "$url" "$pid" dev)" || { ui::fail "Self-test: deployment create failed"; return 2; }

    ui::info "Self-test: waiting for deployment $dep to become running"
    if ! verify::wait_deployment "$url" "$dep" 120; then
        ui::fail "Self-test: deployment $dep didn't reach running in 120s"
        return 2
    fi

    ui::info "Self-test: validating CLI credentials URL is publicly reachable"
    if ! convex_url="$(verify::check_cli_creds "$url" "$dep")"; then
        ui::fail "Self-test: CLI URL check failed"
        return 2
    fi
    ui::success "Self-test passed: $convex_url"

    if (( keep == 0 )); then
        ui::info "Self-test: tearing down demo deployment"
        verify::_curl DELETE "$url/v1/deployments/$dep" >/dev/null || \
            ui::warn "Self-test: demo cleanup returned non-zero (operator can delete via dashboard)"
    fi
    return 0
}
