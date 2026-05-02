# installer/install/wizard.sh
# shellcheck shell=bash
#
# Interactive setup wizard. The thing operators see when they run
#
#     curl -sSf https://raw.../setup.sh | bash
#
# without any flags — a friendly Q&A walks them through the choices
# the install needs (domain, TLS, HA, install dir) and offers to
# auto-fix the dependencies the preflight finds missing (chiefly:
# Docker on a fresh VPS).
#
# Why a separate file: setup.sh is already 900 lines of orchestrator
# + flag parsing. Keeping the conversational UX in its own module
# means we can iterate on the wizard's tone without touching the
# install pipeline, and bats can stub the prompts cleanly.
#
# stdin discipline: a `curl | bash` invocation has stdin pointed at
# the pipe, not the operator's terminal. Every prompt in this file
# therefore reads from /dev/tty directly so the wizard works under
# `curl | sh`. The single early bail-out is wizard::should_run,
# which checks whether /dev/tty is even available — when it isn't
# (CI, headless install) we fall back to the same default-driven
# flow that --non-interactive triggers.
#
# All prompt helpers live under the wizard:: namespace.

# wizard::should_run — true (0) when the wizard is the right thing
# to do RIGHT NOW. Three veto conditions:
#   1. SYNAPSE_NON_INTERACTIVE / --non-interactive set explicitly
#      → operator opted out of prompts.
#   2. /dev/tty isn't readable → no terminal to prompt on (typical
#      CI, packer builds, automated provisioning).
#   3. Any "mode-defining" flag was already passed on the command
#      line (--domain, --no-tls, --base-domain, --enable-ha). The
#      operator clearly knows what they want; the wizard would just
#      ask the same questions back.
#
# All three vetos return 1 (don't run); only the no-veto path
# returns 0 (run the wizard).
wizard::should_run() {
    if [[ -n "${SYNAPSE_NON_INTERACTIVE:-}" ]] || (( ${NON_INTERACTIVE:-0} )); then
        return 1
    fi
    if ! [[ -r /dev/tty ]]; then
        return 1
    fi
    # Mode-defining flags. Each is set by parse_flags in setup.sh —
    # if any is set the operator already answered the wizard's
    # corresponding question. NON-mode flags (--install-dir, --doctor,
    # --no-bootstrap) don't count.
    if [[ -n "${DOMAIN:-}" ]]; then return 1; fi
    if (( ${NO_TLS:-0} )); then return 1; fi
    if [[ -n "${BASE_DOMAIN:-}" ]]; then return 1; fi
    if (( ${ENABLE_HA:-0} )); then return 1; fi
    return 0
}

# wizard::_color — duplicate the ui:: colour init so this file works
# even if sourced before ui.sh (the bats tests sometimes do that).
#
# We force colours ON whenever /dev/tty is readable, even if our own
# stdout has been redirected to a `tee` log (which setup.sh does).
# Without that override the wizard would render plain-text on the
# operator's terminal — visually flat. The escapes still go to /dev/tty
# only via the prompt helpers, so the log file stays clean.
wizard::_color() {
    if [[ "${UI_NO_COLOR:-0}" == "1" ]] || [[ -n "${NO_COLOR:-}" ]]; then
        WIZ_BOLD="" WIZ_DIM="" WIZ_GREEN="" WIZ_BLUE="" WIZ_YELLOW="" WIZ_CYAN="" WIZ_RESET=""
    elif [[ -r /dev/tty ]]; then
        WIZ_BOLD=$'\033[1m'
        WIZ_DIM=$'\033[2m'
        WIZ_GREEN=$'\033[32m'
        WIZ_BLUE=$'\033[34m'
        WIZ_YELLOW=$'\033[33m'
        WIZ_CYAN=$'\033[36m'
        WIZ_RESET=$'\033[0m'
    else
        WIZ_BOLD="" WIZ_DIM="" WIZ_GREEN="" WIZ_BLUE="" WIZ_YELLOW="" WIZ_CYAN="" WIZ_RESET=""
    fi
}

# wizard::_banner — ASCII title card. Printed at the very top of the
# wizard so the operator knows they're in the friendly path (not the
# scary scripted-install path). Colours render via /dev/tty when the
# terminal supports them; falls through to plain text otherwise.
wizard::_banner() {
    wizard::_color
    cat >/dev/tty <<EOF

  ${WIZ_CYAN}╭─────────────────────────────────────────────────────────╮${WIZ_RESET}
  ${WIZ_CYAN}│${WIZ_RESET}                                                         ${WIZ_CYAN}│${WIZ_RESET}
  ${WIZ_CYAN}│${WIZ_RESET}    ${WIZ_BOLD}Synapse${WIZ_RESET} ${WIZ_DIM}— open-source Convex control plane${WIZ_RESET}      ${WIZ_CYAN}│${WIZ_RESET}
  ${WIZ_CYAN}│${WIZ_RESET}    ${WIZ_DIM}installer v${INSTALLER_VERSION}${WIZ_RESET}                                  ${WIZ_CYAN}│${WIZ_RESET}
  ${WIZ_CYAN}│${WIZ_RESET}                                                         ${WIZ_CYAN}│${WIZ_RESET}
  ${WIZ_CYAN}╰─────────────────────────────────────────────────────────╯${WIZ_RESET}

  ${WIZ_DIM}I'll ask a few questions, then handle the rest.${WIZ_RESET}
  ${WIZ_DIM}Press Enter to accept the highlighted default at any prompt.${WIZ_RESET}

EOF
}

# wizard::_step <index> <total> <title>
# Prints a step header so the operator can see how far along they are.
wizard::_step() {
    local idx="$1" total="$2" title="$3"
    wizard::_color
    printf '\n  %s┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄%s\n' \
        "$WIZ_DIM" "$WIZ_RESET" >/dev/tty
    printf '  %sStep %d/%d%s  %s%s%s\n' \
        "$WIZ_DIM" "$idx" "$total" "$WIZ_RESET" \
        "$WIZ_BOLD" "$title" "$WIZ_RESET" >/dev/tty
}

# wizard::ask_yn <prompt> [default=Y]
# Reads y/n/Y/N (or Enter for default) from /dev/tty. Returns 0 for
# yes, 1 for no. Loops on garbage input.
wizard::ask_yn() {
    local prompt="$1" default="${2:-Y}" hint ans
    wizard::_color
    case "$default" in
        Y|y) hint="${WIZ_DIM}[Y/n]${WIZ_RESET}" ;;
        N|n) hint="${WIZ_DIM}[y/N]${WIZ_RESET}" ;;
        *)   hint="${WIZ_DIM}[y/n]${WIZ_RESET}" ;;
    esac
    while :; do
        printf '\n  %s?%s %s %s ' "$WIZ_BLUE" "$WIZ_RESET" "$prompt" "$hint" >/dev/tty
        IFS= read -r ans </dev/tty || ans=""
        ans="${ans:-$default}"
        case "$ans" in
            Y|y|yes|Yes|YES) return 0 ;;
            N|n|no|No|NO)    return 1 ;;
            *) printf '  %s!%s please answer y or n\n' "$WIZ_YELLOW" "$WIZ_RESET" >/dev/tty ;;
        esac
    done
}

# wizard::ask_choice <prompt> <default-index> <option ...>
# Numbered menu. default-index is 1-based and is the value used when
# the operator just hits Enter. Echoes the chosen option to stdout.
#
# Why not arrow-key navigation: portable bash + ANSI cursor handling
# is a pile of edge cases (terminfo, raw mode, signal-safe input).
# Numbered choice with a sane default works on every Ubuntu/Debian/
# RHEL VPS that exists and is what most CLI installers (pacman,
# apt's interactive mode, debconf low) actually use.
wizard::ask_choice() {
    local prompt="$1" default_idx="$2"; shift 2
    local options=("$@")
    local n="${#options[@]}"
    wizard::_color
    printf '\n  %s?%s %s\n' "$WIZ_BLUE" "$WIZ_RESET" "$prompt" >/dev/tty
    local i
    for (( i=0; i<n; i++ )); do
        local marker="  "
        if (( i + 1 == default_idx )); then
            marker="${WIZ_GREEN}›${WIZ_RESET} "
        fi
        printf '    %s%d) %s\n' "$marker" "$((i+1))" "${options[i]}" >/dev/tty
    done
    while :; do
        printf '  %s[1-%d, default %d]%s ' \
            "$WIZ_DIM" "$n" "$default_idx" "$WIZ_RESET" >/dev/tty
        local ans
        IFS= read -r ans </dev/tty || ans=""
        ans="${ans:-$default_idx}"
        if [[ "$ans" =~ ^[0-9]+$ ]] && (( ans >= 1 && ans <= n )); then
            printf '%s' "${options[ans-1]}"
            return 0
        fi
        printf '  %s!%s pick a number from 1 to %d\n' \
            "$WIZ_YELLOW" "$WIZ_RESET" "$n" >/dev/tty
    done
}

# wizard::ask_text <prompt> [default] [validator-fn]
# Free-form text input. validator-fn (optional) takes the answer and
# returns 0 if it's good, non-zero otherwise. Re-prompts on rejection.
# Echoes the accepted value to stdout.
wizard::ask_text() {
    local prompt="$1" default="${2:-}" validator="${3:-}"
    wizard::_color
    while :; do
        if [[ -n "$default" ]]; then
            printf '\n  %s?%s %s %s[%s]%s ' \
                "$WIZ_BLUE" "$WIZ_RESET" "$prompt" \
                "$WIZ_DIM" "$default" "$WIZ_RESET" >/dev/tty
        else
            printf '\n  %s?%s %s ' "$WIZ_BLUE" "$WIZ_RESET" "$prompt" >/dev/tty
        fi
        local ans
        IFS= read -r ans </dev/tty || ans=""
        ans="${ans:-$default}"
        if [[ -z "$validator" ]] || "$validator" "$ans"; then
            printf '%s' "$ans"
            return 0
        fi
    done
}

# wizard::valid_domain — accepts hostnames like "foo.bar.com",
# rejects empty / spaces / leading-dot. Permissive on the spec —
# DNS preflight is the real test.
wizard::valid_domain() {
    local d="$1"
    [[ -n "$d" ]] && [[ "$d" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]] && [[ "$d" == *.* ]]
}

# wizard::valid_path — path that's absolute and not "/". Doesn't
# require the directory to exist; setup.sh handles creation.
wizard::valid_path() {
    local p="$1"
    [[ "$p" == /* ]] && [[ "$p" != "/" ]]
}

# wizard::install_docker_offer — when preflight reports docker
# missing, decide what to do. Returns 0 if the operator said "yes,
# install" (caller should run wizard::install_docker), 1 if they
# said no (caller should print the manual command + abort).
wizard::install_docker_offer() {
    wizard::_color
    cat >/dev/tty <<EOF

  ${WIZ_YELLOW}!${WIZ_RESET} Docker isn't installed on this server.
    Synapse runs everything (control plane + Convex backends) under Docker,
    so we need it. The official one-line installer at
    ${WIZ_DIM}https://get.docker.com${WIZ_RESET} sets it up in ~1 minute.
EOF
    if wizard::ask_yn "Install Docker now (recommended)?" Y; then
        return 0
    fi
    cat >/dev/tty <<EOF

  ${WIZ_YELLOW}!${WIZ_RESET} OK — install Docker manually, then re-run this script:
        ${WIZ_BOLD}curl -fsSL https://get.docker.com | sh${WIZ_RESET}

EOF
    return 1
}

# wizard::install_docker — runs the official Docker install script.
# Streams output to the install log, shows a spinner. Returns the
# script's exit code.
wizard::install_docker() {
    if ! command -v curl >/dev/null 2>&1; then
        ui::fail "wizard: curl not on PATH; can't install Docker"
        return 2
    fi
    ui::spin "Installing Docker via get.docker.com" \
        bash -c 'curl -fsSL https://get.docker.com | sh'
    local rc=$?
    if (( rc == 0 )); then
        # Make sure the daemon is up and enabled. systemctl on
        # WSL/containers will fail silently — that's fine, the
        # subsequent `docker info` probe in preflight reports it.
        systemctl enable --now docker >/dev/null 2>&1 || true
    fi
    return $rc
}

# wizard::run — main flow. Populates these globals (read by setup.sh
# later in main()):
#   DOMAIN          fully-qualified host with DNS pointed at this VPS
#                   (when the operator picks the "with-TLS" path)
#   NO_TLS          1 = operator wants plain HTTP (no domain / not yet)
#   BASE_DOMAIN     wildcard host for custom-domain mode (optional)
#   ENABLE_HA       1 = operator wants HA replicas (advanced)
#   INSTALL_DIR     install path; defaults to /opt/synapse
#   SYNAPSE_AUTO_INSTALL_DOCKER  exported so phase_install_deps acts
#
# Each global is left ALONE if it's already set (operator passed a
# flag covering it). The wizard only fills gaps.
wizard::run() {
    wizard::_color
    wizard::_banner

    # 1. Domain question — three branches:
    #    a) "Yes, I have a domain"  → ask for the host, run --domain= flow
    #    b) "No, just test on this IP" → --no-tls flow
    #    c) "Not sure" → auto-detect public IP, suggest no-tls
    if [[ -z "${DOMAIN:-}" ]] && (( ${NO_TLS:-0} == 0 )); then
        wizard::_step 1 4 "Domain & TLS"
        local domain_choice
        domain_choice="$(wizard::ask_choice \
            "Do you have a domain pointing at this server?" 2 \
            "Yes, I'll type it" \
            "No, just test on this IP (plain HTTP)" \
            "Not sure, check for me")"
        # Order matters: "Not sure*" must come before "No*", otherwise
        # the latter's pattern eats the former on a glob match (both
        # start with N). Shellcheck SC2221/SC2222 catches it; the
        # tests in wizard.bats lock the contract.
        case "$domain_choice" in
            Yes*)
                local typed
                typed="$(wizard::ask_text "Domain" "" wizard::valid_domain)"
                DOMAIN="$typed"
                NO_TLS=0
                ;;
            "Not sure"*)
                wizard::_dont_know_domain_path
                ;;
            No*)
                NO_TLS=1
                # Even on the explicit "No" branch we still want to
                # detect the public IP so the summary can show a
                # ready-to-paste URL instead of "<this-server>".
                wizard::_detect_public_ip
                ;;
        esac
    fi

    # 2. Custom domains (wildcard) — only meaningful with a domain.
    #    Quietly skip when NO_TLS or DOMAIN unset.
    if [[ -n "${DOMAIN:-}" ]] && [[ -z "${BASE_DOMAIN:-}" ]] && (( ${NO_TLS:-0} == 0 )); then
        if wizard::ask_yn "Configure custom domains (https://<deployment>.<base-host>)?" N; then
            local base
            base="$(wizard::ask_text "Wildcard host (e.g. apps.example.com)" "" wizard::valid_domain)"
            BASE_DOMAIN="$base"
        fi
    fi

    # 3. HA mode. Default no — single-replica deployments are the
    #    common path; HA needs an external Postgres + S3 already
    #    provisioned, which a fresh-install operator usually doesn't
    #    have ready.
    if (( ${ENABLE_HA:-0} == 0 )); then
        wizard::_step 2 4 "Deployment mode"
        if wizard::ask_yn \
                "Enable HA mode? (advanced — 2 replicas + external Postgres + S3)" N; then
            ENABLE_HA=1
        fi
    fi

    # 4. Install directory. Default /opt/synapse — almost everyone
    #    keeps it there; the prompt is mainly so the small minority
    #    with a different convention can override without flag-spelunking.
    if [[ -z "${INSTALL_DIR:-}" ]]; then
        wizard::_step 3 4 "Install location"
        INSTALL_DIR="$(wizard::ask_text \
            "Install directory" "/opt/synapse" wizard::valid_path)"
    fi

    # 5. Auto-install dependencies — set the flag here; the phase
    #    that actually installs Docker checks this and the preflight
    #    output. Default Y means "yes, fix what's missing."
    if [[ -z "${SYNAPSE_AUTO_INSTALL_DOCKER:-}" ]]; then
        wizard::_step 4 4 "Dependencies"
        if wizard::ask_yn "Auto-install missing dependencies (Docker etc)?" Y; then
            SYNAPSE_AUTO_INSTALL_DOCKER=1
        else
            SYNAPSE_AUTO_INSTALL_DOCKER=0
        fi
    fi
    export SYNAPSE_AUTO_INSTALL_DOCKER

    # 6. Summary + final confirm. The operator can back out here
    #    without anything having been touched on the system yet.
    wizard::_summary
    if ! wizard::ask_yn "Proceed?" Y; then
        ui::fail "Aborted by operator at the wizard summary"
        exit 130
    fi
}

# wizard::_detect_public_ip — best-effort IP probe via api.ipify.org.
# Stamps SYNAPSE_DETECTED_PUBLIC_IP for the summary. Silent on failure
# (the summary falls back to a placeholder URL, which is fine).
wizard::_detect_public_ip() {
    if [[ -n "${SYNAPSE_DETECTED_PUBLIC_IP:-}" ]]; then
        return 0
    fi
    if ! command -v curl >/dev/null 2>&1; then
        return 0
    fi
    SYNAPSE_DETECTED_PUBLIC_IP="$(curl -sf --max-time 5 https://api.ipify.org 2>/dev/null || echo "")"
    export SYNAPSE_DETECTED_PUBLIC_IP
}

# wizard::_dont_know_domain_path — the "not sure" branch of the
# domain question. We probe public IP, tell the operator what we
# found, and default them onto the no-tls path (the safest landing).
wizard::_dont_know_domain_path() {
    wizard::_color
    wizard::_detect_public_ip
    if [[ -n "${SYNAPSE_DETECTED_PUBLIC_IP:-}" ]]; then
        cat >/dev/tty <<EOF
    ${WIZ_GREEN}✓${WIZ_RESET} This server's public IP: ${WIZ_BOLD}${SYNAPSE_DETECTED_PUBLIC_IP}${WIZ_RESET}
      Without a domain, Synapse will be reachable at:
        ${WIZ_BOLD}http://${SYNAPSE_DETECTED_PUBLIC_IP}:6790${WIZ_RESET}  ${WIZ_DIM}(dashboard)${WIZ_RESET}
        ${WIZ_BOLD}http://${SYNAPSE_DETECTED_PUBLIC_IP}:8080${WIZ_RESET}  ${WIZ_DIM}(API)${WIZ_RESET}
      ${WIZ_DIM}No HTTPS in this mode. Rerun with a domain later for production.${WIZ_RESET}
EOF
    else
        cat >/dev/tty <<EOF
    ${WIZ_YELLOW}!${WIZ_RESET} Couldn't reach api.ipify.org to detect public IP.
      Going with plain-HTTP mode on :6790 and :8080.
EOF
    fi
    NO_TLS=1
}

# wizard::_summary — recap what we've decided so the operator can
# bail if they spot something wrong. Uses a coloured box so it's
# distinct from the question prompts above.
wizard::_summary() {
    wizard::_color
    local mode_line url
    if (( ${NO_TLS:-0} )); then
        mode_line="${WIZ_YELLOW}plain HTTP${WIZ_RESET} ${WIZ_DIM}(no TLS)${WIZ_RESET}"
        if [[ -n "${SYNAPSE_DETECTED_PUBLIC_IP:-}" ]]; then
            url="${WIZ_BOLD}http://${SYNAPSE_DETECTED_PUBLIC_IP}:6790${WIZ_RESET}"
        else
            url="${WIZ_DIM}http://<this-server>:6790${WIZ_RESET}"
        fi
    else
        mode_line="${WIZ_GREEN}HTTPS${WIZ_RESET} ${WIZ_DIM}via Caddy + Let's Encrypt${WIZ_RESET}"
        url="${WIZ_BOLD}https://${DOMAIN}${WIZ_RESET}"
    fi
    local ha_state ha_dim
    if (( ${ENABLE_HA:-0} )); then
        ha_state="${WIZ_GREEN}on${WIZ_RESET}"
        ha_dim="${WIZ_DIM}(2 replicas + external Postgres + S3 per deployment)${WIZ_RESET}"
    else
        ha_state="${WIZ_DIM}off${WIZ_RESET}"
        ha_dim="${WIZ_DIM}(single replica per deployment — common path)${WIZ_RESET}"
    fi
    local deps_state
    if (( ${SYNAPSE_AUTO_INSTALL_DOCKER:-1} )); then
        deps_state="${WIZ_GREEN}auto${WIZ_RESET} ${WIZ_DIM}(install Docker if missing)${WIZ_RESET}"
    else
        deps_state="${WIZ_DIM}manual${WIZ_RESET}"
    fi
    cat >/dev/tty <<EOF

  ${WIZ_CYAN}╭── Summary ──────────────────────────────────────────────╮${WIZ_RESET}
  ${WIZ_CYAN}│${WIZ_RESET}  Mode         ${mode_line}
  ${WIZ_CYAN}│${WIZ_RESET}  URL          ${url}
  ${WIZ_CYAN}│${WIZ_RESET}  Install dir  ${WIZ_BOLD}${INSTALL_DIR}${WIZ_RESET}
  ${WIZ_CYAN}│${WIZ_RESET}  HA mode      ${ha_state}  ${ha_dim}
  ${WIZ_CYAN}│${WIZ_RESET}  Dependencies ${deps_state}
EOF
    if [[ -n "${BASE_DOMAIN:-}" ]]; then
        printf '  %s│%s  Base domain  %s%s%s %s(wildcard *.%s)%s\n' \
            "$WIZ_CYAN" "$WIZ_RESET" \
            "$WIZ_BOLD" "$BASE_DOMAIN" "$WIZ_RESET" \
            "$WIZ_DIM" "$BASE_DOMAIN" "$WIZ_RESET" >/dev/tty
    fi
    printf '  %s╰─────────────────────────────────────────────────────────╯%s\n' \
        "$WIZ_CYAN" "$WIZ_RESET" >/dev/tty
}
