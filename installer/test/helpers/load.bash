# installer/test/helpers/load.bash
# shellcheck shell=bash
#
# Sourced from every .bats file at the top of `setup`. Provides:
#   - bats-{support,assert,file} libraries (bundled in bats/bats:latest)
#   - mock_cmd / unmock_cmd helpers (PATH-shadow pattern)
#   - paths to the lib under test and the fixtures directory
#
# The PATH-shadow mock is the canonical bats pattern for faking
# external commands (docker, ss, df, uname) without requiring a
# heavy-weight container fixture for every test. See
# bats-core docs § "Writing tests / Mocking commands".

bats_load_library bats-support
bats_load_library bats-assert
bats_load_library bats-file

# Repo-relative paths. BATS_TEST_DIRNAME is the directory holding the
# .bats file currently running. We always invoke bats from the repo
# root, so going up two levels from installer/test/lib/ lands at the
# repo root; INSTALLER_DIR points at installer/.
INSTALLER_DIR="$(cd "$BATS_TEST_DIRNAME/../.." && pwd)"
# shellcheck disable=SC2034  # Consumed by .bats files that source this helper.
INSTALLER_LIB="$INSTALLER_DIR/lib"
# shellcheck disable=SC2034  # Consumed by .bats files that source this helper.
INSTALLER_FIXTURES="$INSTALLER_DIR/test/fixtures"

# synapse_mock_setup — Initialises a per-test sandbox of mocked
# commands. Prepended to PATH so a real binary still wins ONLY when no
# mock is registered for the same name. Call from `setup()` in every
# .bats file that uses mock_cmd.
synapse_mock_setup() {
    SYN_MOCK_BIN="$BATS_TEST_TMPDIR/mock-bin"
    SYN_MOCK_CALLS="$BATS_TEST_TMPDIR/mock-calls"
    mkdir -p "$SYN_MOCK_BIN" "$SYN_MOCK_CALLS"
    PATH="$SYN_MOCK_BIN:$PATH"
}

# mock_cmd <name> [exit_code=0] [stdout=""]
# Drops a fake binary on PATH that:
#   - records its argv to $SYN_MOCK_CALLS/<name> (one line per arg)
#   - prints `stdout` verbatim with a trailing newline so consumers
#     using `wc -l` / `awk` see one line per echo, the way real shell
#     commands behave (multi-line safe — stored in a sidecar file so
#     we don't fight heredoc-inside-heredoc quoting)
#   - exits with `exit_code`
mock_cmd() {
    local name="$1" rc="${2:-0}" out="${3:-}"
    local out_file="$SYN_MOCK_BIN/.${name}.out"
    if [[ -n "$out" ]]; then
        printf '%s\n' "$out" >"$out_file"
    else
        : >"$out_file"
    fi
    cat >"$SYN_MOCK_BIN/$name" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$@" >>"$SYN_MOCK_CALLS/$name"
cat "$out_file"
exit $rc
EOF
    chmod +x "$SYN_MOCK_BIN/$name"
}

# unmock_cmd <name> — Removes a previously-registered mock. Useful when
# a test needs to assert "this command would be missing".
unmock_cmd() {
    rm -f "$SYN_MOCK_BIN/$1" "$SYN_MOCK_BIN/.${1}.out"
}
