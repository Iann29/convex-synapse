# installer/install/updater.sh
# shellcheck shell=bash
#
# phase_install_updater — drops the synapse-updater daemon (Python 3
# HTTP server over a unix socket) and its systemd unit on the host so
# /v1/admin/upgrade in the dashboard can trigger setup.sh --upgrade.
#
# Lives outside docker compose because the daemon's job is to recreate
# the compose stack — running it in compose would have it killing
# itself mid-upgrade. systemd is the survival mechanism.
#
# Lives in its own file (not setup.sh) so bats can source it standalone
# without dragging the whole installer's top-level state along.

phase_install_updater() {
    # CURRENT_STEP is consumed by setup.sh's ERR/EXIT trap to identify
    # which phase failed. Set even when sourced standalone; harmless.
    # shellcheck disable=SC2034
    CURRENT_STEP="install_updater"
    ui::step "Installing self-update daemon"

    local prefix=""
    prefix="$(detect::sudo_cmd 2>/dev/null || true)"

    # Skip cleanly when there's no systemd (bats CI, container hosts,
    # weird Linux distros). The dashboard's upgrade button degrades to
    # a "SSH and run setup.sh --upgrade" hint when /status is unreachable.
    if ! detect::has_cmd systemctl; then
        ui::warn "systemd not available — skipping self-update daemon (operators on this host upgrade via SSH only)"
        return 0
    fi
    if ! detect::has_cmd python3; then
        ui::warn "python3 not on PATH — skipping self-update daemon"
        return 0
    fi

    local src_bin="$INSTALL_DIR/installer/updater/synapse-updater"
    local src_unit="$INSTALL_DIR/installer/updater/synapse-updater.service"
    if [[ ! -f "$src_bin" || ! -f "$src_unit" ]]; then
        ui::warn "updater bundle not found at $INSTALL_DIR/installer/updater — skipping"
        return 0
    fi

    # Stamp INSTALL_DIR/VERSION so the daemon's /version endpoint can
    # report something authoritative (otherwise it falls back to
    # `git describe`, which fails in the tarball-install case where
    # there's no .git present).
    echo "$INSTALLER_VERSION" | $prefix tee "$INSTALL_DIR/VERSION" >/dev/null

    $prefix install -m 0755 "$src_bin" /usr/local/bin/synapse-updater
    $prefix install -m 0644 "$src_unit" /etc/systemd/system/synapse-updater.service
    $prefix systemctl daemon-reload
    $prefix systemctl enable --now synapse-updater >/dev/null 2>&1 || true
    # If the service was already running with an older binary, restart
    # so the new code is picked up. SKIP this when called from inside
    # the running updater — restarting the daemon mid-upgrade would
    # kill the very process orchestrating the run, and the dashboard's
    # /status polling would lose visibility.
    if [[ "${SYNAPSE_UPDATER_NO_RESTART:-0}" == "1" ]]; then
        ui::info "Updater binary refreshed (restart skipped — operator should 'systemctl restart synapse-updater' after upgrade completes to load the new code)"
    else
        $prefix systemctl restart synapse-updater >/dev/null 2>&1 || true
    fi

    # Health probe — give it a couple of seconds to bind the socket.
    local socket=/run/synapse/updater.sock
    local tries=10
    while (( tries-- > 0 )); do
        if [[ -S "$socket" ]] && $prefix curl -sSf --unix-socket "$socket" http://x/healthz >/dev/null 2>&1; then
            ui::success "Self-update daemon is up (unix:$socket)"
            return 0
        fi
        sleep 0.3
    done
    ui::warn "Self-update daemon installed but health probe failed — check 'systemctl status synapse-updater'"
}
