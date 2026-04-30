# installer/install/ui.sh
# shellcheck shell=bash
#
# Operator-facing UI helpers — colored output, spinner, prompts. Every
# user-visible line in setup.sh goes through one of these so we have a
# single place to control color, format, and TTY behavior.
#
# Color discipline:
#   - ANSI escapes are emitted ONLY when stdout is a TTY ([[ -t 1 ]]).
#     This keeps the install log clean when invoked under `tee`,
#     redirected to a file, or piped (`curl | sh`).
#   - $NO_COLOR (https://no-color.org) and $UI_NO_COLOR=1 force
#     colorless output; $UI_FORCE_COLOR=1 forces colors on (useful
#     when you know the consumer renders ANSI but isn't a TTY, e.g.
#     piping through `less -R`).
#
# Function namespace `ui::` to keep these from colliding with the
# caller's globals.

# Lazy color initialization. Re-evaluated on every call so callers can
# flip UI_NO_COLOR / UI_FORCE_COLOR mid-script (e.g. when bringing up
# `docker compose` we want to silence ANSI even though stdout is a TTY,
# because compose's own progress bars conflict with our spinner).
ui::_color_init() {
    if [[ "${UI_NO_COLOR:-0}" == "1" ]] || [[ -n "${NO_COLOR:-}" ]]; then
        UI_GREEN="" UI_YELLOW="" UI_RED="" UI_CYAN="" UI_BOLD="" UI_RESET=""
    elif [[ "${UI_FORCE_COLOR:-0}" == "1" ]] || [[ -t 1 ]]; then
        UI_GREEN=$'\033[32m'
        UI_YELLOW=$'\033[33m'
        UI_RED=$'\033[31m'
        UI_CYAN=$'\033[36m'
        UI_BOLD=$'\033[1m'
        UI_RESET=$'\033[0m'
    else
        UI_GREEN="" UI_YELLOW="" UI_RED="" UI_CYAN="" UI_BOLD="" UI_RESET=""
    fi
}

# ui::success/warn/fail/info — single-line status output. fail goes to
# stderr; the rest go to stdout so the operator can `2>/dev/null` to
# silence non-error noise and still see what broke.
ui::success() { ui::_color_init; printf '%s✓%s %s\n' "$UI_GREEN"  "$UI_RESET" "$*"; }
ui::warn()    { ui::_color_init; printf '%s!%s %s\n' "$UI_YELLOW" "$UI_RESET" "$*"; }
ui::fail()    { ui::_color_init; printf '%s✗%s %s\n' "$UI_RED"    "$UI_RESET" "$*" >&2; }
ui::info()    { ui::_color_init; printf '%sℹ%s %s\n' "$UI_CYAN"   "$UI_RESET" "$*"; }

# ui::step — phase header (Coolify "Step X/N" pattern). Prefix line
# blank for visual separation, bold + cyan arrow.
ui::step() {
    ui::_color_init
    printf '\n%s==>%s %s%s%s\n' "$UI_CYAN" "$UI_RESET" "$UI_BOLD" "$*" "$UI_RESET"
}

# ui::confirm <prompt> [default=N]
# Yes/No prompt. Returns 0 (yes) or 1 (no). When SYNAPSE_NON_INTERACTIVE
# is set to a non-empty value the prompt is skipped and the default is
# used — which is what `curl | sh --non-interactive` consumers need.
ui::confirm() {
    local prompt="$1" default="${2:-N}" hint ans
    if [[ "$default" =~ ^[Yy]$ ]]; then
        hint="[Y/n]"
    else
        hint="[y/N]"
    fi
    if [[ -n "${SYNAPSE_NON_INTERACTIVE:-}" ]]; then
        [[ "$default" =~ ^[Yy]$ ]]
        return $?
    fi
    read -r -p "$prompt $hint " ans
    ans="${ans:-$default}"
    [[ "$ans" =~ ^[Yy]$ ]]
}

# ui::spin <message> <command...>
# Runs `command` with its args; on a TTY shows a Braille spinner with
# the message, replacing the line with ✓/✗ when the command exits.
# Off-TTY (CI logs, redirected output) emits a "info … success/fail"
# pair instead so log-greppers see structured events.
#
# Propagates the command's exit code. The function uses `wait` to pick
# up the exit code from the backgrounded process; on a SIGINT/SIGTERM
# while the spinner runs, the trap in setup.sh handles cleanup.
ui::spin() {
    local msg="$1"; shift
    if [[ ! -t 1 ]]; then
        ui::info "$msg"
        local rc=0
        "$@" || rc=$?
        if (( rc == 0 )); then
            ui::success "$msg"
        else
            ui::fail "$msg (exit $rc)"
        fi
        return $rc
    fi
    local frames='⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏'
    "$@" &
    local pid=$!
    local i=0
    # `kill -0` is the standard "is this process still running?" probe.
    # Fails (and exits the loop) when pid has reaped.
    while kill -0 "$pid" 2>/dev/null; do
        local frame="${frames:i++%${#frames}:1}"
        printf '\r%s %s' "$frame" "$msg"
        sleep 0.08
    done
    local rc=0
    wait "$pid" || rc=$?
    # \033[K clears from cursor to end-of-line so leftover spinner
    # characters don't smear the success line.
    printf '\r\033[K'
    if (( rc == 0 )); then
        ui::success "$msg"
    else
        ui::fail "$msg (exit $rc)"
    fi
    return $rc
}
