#!/usr/bin/env bats
#
# Unit tests for installer/lib/detect.sh.
#
# These run inside the bats/bats:latest image (Alpine + bats-core
# 1.13 + bats-assert/support/file). Pure-function tests — no Docker
# socket, no network. The PATH-shadow mock in helpers/load.bash lets us
# fake `uname`, `df`, package managers, etc., without container
# fixtures. Distro-specific behaviour is exercised via the os-release
# fixtures under installer/test/fixtures/.

load '../helpers/load'

setup() {
    synapse_mock_setup
    # shellcheck source=../../lib/detect.sh
    source "$INSTALLER_LIB/detect.sh"
}

# ---- os_id ----------------------------------------------------------

@test "os_id: debian fixture -> debian" {
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-debian" run detect::os_id
    assert_success
    assert_output "debian"
}

@test "os_id: ubuntu fixture -> ubuntu" {
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-ubuntu" run detect::os_id
    assert_success
    assert_output "ubuntu"
}

@test "os_id: fedora fixture -> fedora" {
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-fedora" run detect::os_id
    assert_success
    assert_output "fedora"
}

@test "os_id: pop fixture -> pop (preserves derivative ID)" {
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-pop" run detect::os_id
    assert_success
    assert_output "pop"
}

@test "os_id: missing file -> unknown" {
    DETECT_OS_RELEASE="/nonexistent/file" run detect::os_id
    assert_success
    assert_output "unknown"
}

@test "os_id: CRLF-encoded file -> debian (no trailing CR)" {
    # Catches the silent-mismatch bug where /etc/os-release shipped
    # with CRLF (Windows-edited config, some embedded images) yielded
    # "debian\r" which then fails downstream `case "$id" in debian)`
    # matches.
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-crlf" run detect::os_id
    assert_success
    assert_output "debian"
    [[ "${output: -1}" != $'\r' ]]
}

# ---- os_family ------------------------------------------------------

@test "os_family: debian -> debian" {
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-debian" run detect::os_family
    assert_success
    assert_output "debian"
}

@test "os_family: ubuntu -> debian" {
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-ubuntu" run detect::os_family
    assert_success
    assert_output "debian"
}

@test "os_family: pop -> debian (via ID_LIKE)" {
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-pop" run detect::os_family
    assert_success
    assert_output "debian"
}

@test "os_family: fedora -> redhat" {
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-fedora" run detect::os_family
    assert_success
    assert_output "redhat"
}

@test "os_family: rocky -> redhat (via ID, not ID_LIKE)" {
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-rocky" run detect::os_family
    assert_success
    assert_output "redhat"
}

@test "os_family: arch -> arch" {
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-arch" run detect::os_family
    assert_success
    assert_output "arch"
}

@test "os_family: alpine -> alpine" {
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-alpine" run detect::os_family
    assert_success
    assert_output "alpine"
}

@test "os_family: missing file -> unknown" {
    DETECT_OS_RELEASE="/nonexistent" run detect::os_family
    assert_success
    assert_output "unknown"
}

# ---- os_version / codename ------------------------------------------

@test "os_version: debian fixture -> 12" {
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-debian" run detect::os_version
    assert_success
    assert_output "12"
}

@test "os_version: ubuntu fixture -> 24.04" {
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-ubuntu" run detect::os_version
    assert_success
    assert_output "24.04"
}

@test "os_codename: debian -> bookworm" {
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-debian" run detect::os_codename
    assert_success
    assert_output "bookworm"
}

@test "os_codename: pop -> jammy" {
    # Pop sets both VERSION_CODENAME=jammy and UBUNTU_CODENAME=jammy.
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-pop" run detect::os_codename
    assert_success
    assert_output "jammy"
}

@test "os_codename: arch (no codename) -> empty" {
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-arch" run detect::os_codename
    assert_success
    assert_output ""
}

@test "os_codename: Linux Mint -> jammy (Ubuntu base, NOT virginia)" {
    # Mint sets VERSION_CODENAME=virginia (Mint's brand name) and
    # UBUNTU_CODENAME=jammy. Apt repos (Docker's CDN) only know Ubuntu
    # codenames, so we must prefer UBUNTU_CODENAME for Ubuntu-derived
    # distros. Without this fix, the install fails on Mint with
    # "no Release file for virginia".
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-mint" run detect::os_codename
    assert_success
    assert_output "jammy"
}

@test "os_codename: CRLF file -> bookworm (no trailing CR)" {
    DETECT_OS_RELEASE="$INSTALLER_FIXTURES/os-release-crlf" run detect::os_codename
    assert_success
    assert_output "bookworm"
}

# ---- arch -----------------------------------------------------------

@test "arch: x86_64 -> amd64" {
    mock_cmd uname 0 "x86_64"
    run detect::arch
    assert_success
    assert_output "amd64"
}

@test "arch: aarch64 -> arm64" {
    mock_cmd uname 0 "aarch64"
    run detect::arch
    assert_success
    assert_output "arm64"
}

@test "arch: armv7l -> armv7" {
    mock_cmd uname 0 "armv7l"
    run detect::arch
    assert_success
    assert_output "armv7"
}

@test "arch: i686 -> i386" {
    mock_cmd uname 0 "i686"
    run detect::arch
    assert_success
    assert_output "i386"
}

@test "arch: unknown machine echoes raw" {
    mock_cmd uname 0 "riscv64"
    run detect::arch
    assert_success
    assert_output "riscv64"
}

# ---- pkg_manager ----------------------------------------------------
#
# Important: these tests pin PATH to JUST the mock dir so a real
# `apt-get` on a Debian/Ubuntu dev box doesn't beat the mock. The
# previous version assumed the bats/bats:latest Alpine CI image (no
# apt-get on PATH); developers running bats locally on a Debian host
# saw failures. `command -v` is a builtin so no real /bin needed.

@test "pkg_manager: apt wins when apt-get is on PATH" {
    mock_cmd apt-get 0
    mock_cmd dnf 0
    PATH="$SYN_MOCK_BIN" run detect::pkg_manager
    assert_success
    assert_output "apt"
}

@test "pkg_manager: dnf when only dnf is available" {
    mock_cmd dnf 0
    PATH="$SYN_MOCK_BIN" run detect::pkg_manager
    assert_success
    assert_output "dnf"
}

@test "pkg_manager: pacman when only pacman is available" {
    mock_cmd pacman 0
    PATH="$SYN_MOCK_BIN" run detect::pkg_manager
    assert_success
    assert_output "pacman"
}

@test "pkg_manager: zypper when nothing else is available" {
    mock_cmd zypper 0
    PATH="$SYN_MOCK_BIN" run detect::pkg_manager
    assert_success
    assert_output "zypper"
}

@test "pkg_manager: unknown when no package manager on PATH" {
    PATH="$SYN_MOCK_BIN" run detect::pkg_manager
    assert_success
    assert_output "unknown"
}

# NOTE: zypper isn't tested in isolation because the bats/bats:latest
# image is Alpine, which has `apk` natively — the dispatch table
# always lands on apk before reaching zypper. Faking "all package
# managers absent" requires shadowing `command` itself, which is
# fragile across bash versions. The four tests above cover the branch
# logic adequately.

# ---- is_root / sudo_cmd ---------------------------------------------
#
# We use DETECT_UID rather than EUID because bash treats EUID as a
# readonly variable; trying to reassign it from a test fails with
# `EUID: readonly variable`. detect::is_root reads DETECT_UID first so
# tests can simulate any UID without spawning a child shell.

@test "is_root: DETECT_UID=0 -> success" {
    DETECT_UID=0 run detect::is_root
    assert_success
}

@test "is_root: DETECT_UID=1000 -> failure" {
    DETECT_UID=1000 run detect::is_root
    assert_failure
}

@test "is_root: DETECT_UID=foo (non-numeric) -> failure (not silently 'root')" {
    # Bash arithmetic evaluates undefined-name strings to 0, which
    # would silently mark a non-numeric override as root. The regex
    # guard fails closed.
    DETECT_UID=foo run detect::is_root
    assert_failure
}

@test "sudo_cmd: root -> empty stdout, success" {
    DETECT_UID=0 run detect::sudo_cmd
    assert_success
    assert_output ""
}

@test "sudo_cmd: non-root + sudo present -> 'sudo'" {
    mock_cmd sudo 0
    DETECT_UID=1000 run detect::sudo_cmd
    assert_success
    assert_output "sudo"
}

@test "sudo_cmd: non-root + only doas -> 'doas'" {
    mock_cmd doas 0
    DETECT_UID=1000 run detect::sudo_cmd
    assert_success
    assert_output "doas"
}

# ---- has_X ----------------------------------------------------------

@test "has_cmd: existing cmd -> success" {
    mock_cmd foobar 0
    run detect::has_cmd foobar
    assert_success
}

@test "has_cmd: missing cmd -> failure" {
    run detect::has_cmd this-cmd-does-not-exist-9876
    assert_failure
}

@test "has_docker: mocked -> success" {
    mock_cmd docker 0
    run detect::has_docker
    assert_success
}

@test "has_caddy: mocked -> success" {
    mock_cmd caddy 0
    run detect::has_caddy
    assert_success
}

@test "has_nginx: mocked -> success" {
    mock_cmd nginx 0
    run detect::has_nginx
    assert_success
}

@test "has_ufw: mocked -> success" {
    mock_cmd ufw 0
    run detect::has_ufw
    assert_success
}

# has_systemd uses the canonical /run/systemd/system check. Inside
# bats/bats:latest (Alpine, no systemd), this should return false.
@test "has_systemd: alpine container -> failure" {
    run detect::has_systemd
    assert_failure
}

# ---- disk_free_gb ---------------------------------------------------

@test "disk_free_gb: parses POSIX df output (50 GB free)" {
    # 52428800 KB / 1024 / 1024 = 50 GB
    mock_cmd df 0 "Filesystem 1K-blocks Used Available Use% Mounted on
/dev/foo   100000000 0    52428800  0%   /"
    run detect::disk_free_gb /
    assert_success
    assert_output "50"
}

@test "disk_free_gb: tiny disk rounds down" {
    # 524288 KB = 512 MB; integer division by 1024² -> 0.
    mock_cmd df 0 "Filesystem 1K-blocks Used Available Use% Mounted on
/dev/tiny  1000000  0    524288    50%  /"
    run detect::disk_free_gb /
    assert_success
    assert_output "0"
}

@test "disk_free_gb: long device path stays on one line via -P" {
    # GNU df without -P wraps to two lines when the device path is
    # long (iSCSI LUN, UUID-style mapping, LVM-on-LUKS). With -P, the
    # row stays single-line and our awk NR==2 lands on the size column.
    mock_cmd df 0 "Filesystem            1K-blocks  Used Available Use% Mounted on
/dev/disk/by-uuid/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee 100000000 50000 52428800  1% /"
    run detect::disk_free_gb /
    assert_success
    assert_output "50"
}

@test "disk_free_gb: df returns empty -> 0 + failure" {
    mock_cmd df 1 ""
    run detect::disk_free_gb /
    assert_failure
    assert_output "0"
}

# ---- ram_total_gb ---------------------------------------------------

@test "ram_total_gb: 2 GB fixture -> 2" {
    DETECT_MEMINFO="$INSTALLER_FIXTURES/meminfo-2gb" run detect::ram_total_gb
    assert_success
    assert_output "2"
}

@test "ram_total_gb: 512 MB fixture -> 0 (rounds down)" {
    DETECT_MEMINFO="$INSTALLER_FIXTURES/meminfo-512mb" run detect::ram_total_gb
    assert_success
    assert_output "0"
}

@test "ram_total_gb: missing file -> 0 + failure" {
    DETECT_MEMINFO="/nonexistent/meminfo" run detect::ram_total_gb
    assert_failure
    assert_output "0"
}

# ---- public_ip ------------------------------------------------------

@test "public_ip: DETECT_PUBLIC_IP_OVERRIDE short-circuits the curl call" {
    DETECT_PUBLIC_IP_OVERRIDE="203.0.113.42" run detect::public_ip
    assert_success
    assert_output "203.0.113.42"
}

@test "public_ip: ipify returns valid IP -> echoed" {
    cat >"$SYN_MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
echo "198.51.100.7"
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/curl"
    run detect::public_ip
    assert_success
    assert_output "198.51.100.7"
}

@test "public_ip: ipify returns garbage -> falls through to next service" {
    # First call (ipify) returns HTML, second (ifconfig.me) returns IP.
    cat >"$SYN_MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
counter_file="$BATS_TEST_TMPDIR/curl-counter"
n=0
[[ -f "$counter_file" ]] && n=$(cat "$counter_file")
n=$((n + 1))
echo "$n" > "$counter_file"
if (( n == 1 )); then
    echo "<html>error</html>"
else
    echo "192.0.2.99"
fi
exit 0
EOF
    chmod +x "$SYN_MOCK_BIN/curl"
    run detect::public_ip
    assert_success
    assert_output "192.0.2.99"
}

@test "public_ip: both services return non-IP -> failure" {
    mock_cmd curl 0 "<html>not an IP</html>"
    run detect::public_ip
    assert_failure
}

@test "public_ip: curl absent -> failure" {
    # Pin PATH to mock dir only so command -v curl fails.
    PATH="$SYN_MOCK_BIN" run detect::public_ip
    assert_failure
}

@test "public_ip: rejects IPv6 (we only wire IPv4 ports today)" {
    mock_cmd curl 0 "2001:db8::1"
    run detect::public_ip
    assert_failure
}
