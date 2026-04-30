#!/usr/bin/env bats
#
# Unit tests for installer/install/preflight.sh.
#
# Strategy: source ui.sh + detect.sh + preflight.sh, then redefine the
# detect:: helpers as shell functions in each test to force the
# desired branch. External commands (docker, dig, curl) are mocked via
# the PATH-shadow helper so we don't depend on the host having them.
#
# NOTE: function redefinitions persist across the @test boundary
# inside a single .bats file because all tests share the same shell
# process for setup/teardown context. We re-source preflight.sh in
# setup() to reset state; redefining detect:: helpers in a single
# @test then takes effect just for that test's `run` invocation
# (subshell), so leakage is bounded.

load '../helpers/load'

setup() {
    synapse_mock_setup
    # shellcheck source=../../install/ui.sh
    source "$INSTALLER_DIR/install/ui.sh"
    # shellcheck source=../../lib/detect.sh
    source "$INSTALLER_DIR/lib/detect.sh"
    # shellcheck source=../../install/preflight.sh
    source "$INSTALLER_DIR/install/preflight.sh"
    # Force colorless so output assertions don't fight ANSI.
    UI_NO_COLOR=1
}

# ---- check_os -------------------------------------------------------

@test "check_os: ubuntu (debian family) -> success" {
    detect::os_id()      { echo "ubuntu"; }
    detect::os_family()  { echo "debian"; }
    detect::os_version() { echo "24.04"; }
    run preflight::check_os
    assert_success
    assert_output --partial "OS: ubuntu 24.04"
}

@test "check_os: fedora (redhat family) -> success" {
    detect::os_id()      { echo "fedora"; }
    detect::os_family()  { echo "redhat"; }
    detect::os_version() { echo "40"; }
    run preflight::check_os
    assert_success
    assert_output --partial "OS: fedora 40"
}

@test "check_os: arch -> warn (community-tested)" {
    detect::os_id()      { echo "arch"; }
    detect::os_family()  { echo "arch"; }
    detect::os_version() { echo "rolling"; }
    run preflight::check_os
    assert_failure 1
    assert_output --partial "community-tested"
}

@test "check_os: alpine -> warn" {
    detect::os_id()      { echo "alpine"; }
    detect::os_family()  { echo "alpine"; }
    detect::os_version() { echo "3.20"; }
    run preflight::check_os
    assert_failure 1
}

@test "check_os: unknown family -> fail" {
    detect::os_id()      { echo "freebsd"; }
    detect::os_family()  { echo "unknown"; }
    detect::os_version() { echo "14"; }
    run preflight::check_os
    assert_failure 2
    assert_output --partial "not supported"
}

# ---- check_arch -----------------------------------------------------

@test "check_arch: amd64 -> success" {
    detect::arch() { echo "amd64"; }
    run preflight::check_arch
    assert_success
}

@test "check_arch: arm64 -> success" {
    detect::arch() { echo "arm64"; }
    run preflight::check_arch
    assert_success
}

@test "check_arch: armv7 -> fail" {
    detect::arch() { echo "armv7"; }
    run preflight::check_arch
    assert_failure 2
    assert_output --partial "armv7"
}

@test "check_arch: i386 -> fail" {
    detect::arch() { echo "i386"; }
    run preflight::check_arch
    assert_failure 2
}

# ---- check_sudo -----------------------------------------------------

@test "check_sudo: root -> success ('running as root')" {
    detect::sudo_cmd() { echo ""; return 0; }
    run preflight::check_sudo
    assert_success
    assert_output --partial "running as root"
}

@test "check_sudo: sudo present -> success ('sudo available')" {
    detect::sudo_cmd() { echo "sudo"; return 0; }
    run preflight::check_sudo
    assert_success
    assert_output --partial "sudo available"
}

@test "check_sudo: nothing available -> fail" {
    detect::sudo_cmd() { echo ""; return 1; }
    run preflight::check_sudo
    assert_failure 2
    assert_output --partial "not root"
}

# ---- check_docker ---------------------------------------------------

@test "check_docker: docker missing -> warn with install hint" {
    detect::has_docker() { return 1; }
    run preflight::check_docker
    assert_failure 1
    assert_output --partial "not installed"
    assert_output --partial "get.docker.com"
}

@test "check_docker: daemon unreachable -> fail" {
    detect::has_docker() { return 0; }
    mock_cmd docker 1 ""    # docker info returns 1
    run preflight::check_docker
    assert_failure 2
    assert_output --partial "daemon unreachable"
}

@test "check_docker: too old -> fail" {
    detect::has_docker() { return 0; }
    # Mock docker so `info` succeeds (rc=0) and `version --format` echoes 19.03.
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    info) exit 0 ;;
    version) echo "19.03.5" ;;
esac
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    run preflight::check_docker
    assert_failure 2
    assert_output --partial "20.10+ required"
}

@test "check_docker: 24.0.7 -> success" {
    detect::has_docker() { return 0; }
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    info) exit 0 ;;
    version) echo "24.0.7" ;;
esac
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    run preflight::check_docker
    assert_success
    assert_output --partial "Docker: 24.0.7"
}

# ---- check_compose --------------------------------------------------

@test "check_compose: missing -> fail" {
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
[[ "$1" == "compose" ]] && exit 1
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    run preflight::check_compose
    assert_failure 2
    assert_output --partial "v1 is not supported"
}

@test "check_compose: present -> success" {
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
if [[ "$1 $2" == "compose version" ]]; then
    if [[ "$3" == "--short" ]]; then echo "2.27.0"; else echo "Docker Compose version v2.27.0"; fi
    exit 0
fi
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    run preflight::check_compose
    assert_success
    assert_output --partial "2.27.0"
}

# ---- check_disk -----------------------------------------------------

@test "check_disk: 50 GB free, default need=10 -> success" {
    detect::disk_free_gb() { echo 50; }
    run preflight::check_disk
    assert_success
    assert_output --partial "50 GB free"
}

@test "check_disk: 5 GB free, default need=10 -> fail" {
    detect::disk_free_gb() { echo 5; }
    run preflight::check_disk
    assert_failure 2
    assert_output --partial "5 GB free"
}

@test "check_disk: SYNAPSE_DISK_GB_MIN override -> 5 OK" {
    detect::disk_free_gb() { echo 5; }
    SYNAPSE_DISK_GB_MIN=4 run preflight::check_disk
    assert_success
}

# ---- check_ram ------------------------------------------------------

@test "check_ram: 4 GB total, need 2 -> success" {
    detect::ram_total_gb() { echo 4; }
    run preflight::check_ram
    assert_success
}

@test "check_ram: 1 GB -> warn (tight)" {
    detect::ram_total_gb() { echo 1; }
    run preflight::check_ram
    assert_failure 1
    assert_output --partial "tight"
}

@test "check_ram: 0 GB -> fail" {
    detect::ram_total_gb() { echo 0; }
    run preflight::check_ram
    assert_failure 2
}

# ---- check_outbound -------------------------------------------------

@test "check_outbound: curl missing -> warn" {
    detect::has_cmd() { return 1; }
    run preflight::check_outbound
    assert_failure 1
    assert_output --partial "curl not installed"
}

@test "check_outbound: ghcr reachable -> success" {
    detect::has_cmd() { return 0; }
    mock_cmd curl 0
    run preflight::check_outbound
    assert_success
}

@test "check_outbound: ghcr unreachable -> warn" {
    detect::has_cmd() { return 0; }
    mock_cmd curl 7
    run preflight::check_outbound
    assert_failure 1
    assert_output --partial "not reachable"
}

# ---- check_dns ------------------------------------------------------

@test "check_dns: empty domain -> success (skip)" {
    run preflight::check_dns ""
    assert_success
}

@test "check_dns: dig missing -> warn" {
    detect::has_cmd() { [[ "$1" == "dig" ]] && return 1; return 0; }
    run preflight::check_dns "synapse.example.com"
    assert_failure 1
    assert_output --partial "'dig' not installed"
}

@test "check_dns: domain matches host -> success" {
    detect::has_cmd() { return 0; }
    cat >"$SYN_MOCK_BIN/dig" <<'EOF'
#!/usr/bin/env bash
echo "203.0.113.5"
EOF
    chmod +x "$SYN_MOCK_BIN/dig"
    cat >"$SYN_MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
echo "203.0.113.5"
EOF
    chmod +x "$SYN_MOCK_BIN/curl"
    run preflight::check_dns "synapse.example.com"
    assert_success
    assert_output --partial "matches this host"
}

@test "check_dns: domain mismatches -> warn" {
    detect::has_cmd() { return 0; }
    cat >"$SYN_MOCK_BIN/dig" <<'EOF'
#!/usr/bin/env bash
echo "10.0.0.1"
EOF
    chmod +x "$SYN_MOCK_BIN/dig"
    cat >"$SYN_MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
echo "203.0.113.5"
EOF
    chmod +x "$SYN_MOCK_BIN/curl"
    run preflight::check_dns "synapse.example.com"
    assert_failure 1
    assert_output --partial "Caddy can't get a cert"
}

@test "check_dns: domain has no A record -> warn" {
    detect::has_cmd() { return 0; }
    mock_cmd dig 0 ""
    mock_cmd curl 0 "203.0.113.5"
    run preflight::check_dns "synapse.example.com"
    assert_failure 1
    assert_output --partial "no A record"
}

# ---- run_all --------------------------------------------------------

@test "run_all: all pass -> success" {
    detect::os_id()        { echo "ubuntu"; }
    detect::os_family()    { echo "debian"; }
    detect::os_version()   { echo "24.04"; }
    detect::arch()         { echo "amd64"; }
    detect::sudo_cmd()     { echo ""; return 0; }
    detect::has_docker()   { return 0; }
    detect::has_cmd()      { return 0; }
    detect::disk_free_gb() { echo 100; }
    detect::ram_total_gb() { echo 4; }
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    info) exit 0 ;;
    version) echo "24.0.7" ;;
    compose)
        if [[ "$2" == "version" ]]; then
            if [[ "$3" == "--short" ]]; then echo "2.27"; fi
            exit 0
        fi
        ;;
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    mock_cmd curl 0
    run preflight::run_all
    assert_success
    assert_output --partial "all checks passed"
}

@test "run_all: one fail -> exit 2 with summary" {
    detect::os_id()        { echo "freebsd"; }
    detect::os_family()    { echo "unknown"; }
    detect::os_version()   { echo "14"; }
    detect::arch()         { echo "amd64"; }
    detect::sudo_cmd()     { echo ""; return 0; }
    detect::has_docker()   { return 0; }
    detect::has_cmd()      { return 0; }
    detect::disk_free_gb() { echo 100; }
    detect::ram_total_gb() { echo 4; }
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    info) exit 0 ;;
    version) echo "24.0.7" ;;
    compose) exit 0 ;;
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    mock_cmd curl 0
    run preflight::run_all
    assert_failure 2
    assert_output --partial "fail"
}

@test "run_all: warns only -> success with warning summary" {
    detect::os_id()        { echo "arch"; }
    detect::os_family()    { echo "arch"; }
    detect::os_version()   { echo "rolling"; }
    detect::arch()         { echo "amd64"; }
    detect::sudo_cmd()     { echo ""; return 0; }
    detect::has_docker()   { return 0; }
    detect::has_cmd()      { return 0; }
    detect::disk_free_gb() { echo 100; }
    detect::ram_total_gb() { echo 4; }
    cat >"$SYN_MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
    info) exit 0 ;;
    version) echo "24.0.7" ;;
    compose) exit 0 ;;
esac
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/docker"
    mock_cmd curl 0
    run preflight::run_all
    assert_success
    assert_output --partial "warn"
}
