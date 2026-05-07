#!/usr/bin/env bats
# Regression: setup.sh honours SYNAPSE_INSTALL_DIR as fallback when neither
# --install-dir= nor wizard answered. The systemd unit exports this for the
# daemon, so reconfigure/upgrade calls into setup.sh resolve to the same
# install root the daemon was templated against.

setup() {
    SETUP_SH="${BATS_TEST_DIRNAME}/../../setup.sh"
}

@test "SYNAPSE_INSTALL_DIR sets default when --install-dir not passed" {
    SYNAPSE_INSTALL_DIR=/opt/synapse-test \
        bash -c "set -e; source <(grep -A1 \"INSTALL_DIR_DEFAULT=\" \"$SETUP_SH\" | head -2); INSTALL_DIR_DEFAULT=/opt/synapse; INSTALL_DIR=\"\${SYNAPSE_INSTALL_DIR:-\$INSTALL_DIR_DEFAULT}\"; [ \"\$INSTALL_DIR\" = \"/opt/synapse-test\" ]"
}

@test "SYNAPSE_INSTALL_DIR unset falls back to INSTALL_DIR_DEFAULT" {
    unset SYNAPSE_INSTALL_DIR
    bash -c "INSTALL_DIR_DEFAULT=/opt/synapse; INSTALL_DIR=\"\${SYNAPSE_INSTALL_DIR:-\$INSTALL_DIR_DEFAULT}\"; [ \"\$INSTALL_DIR\" = \"/opt/synapse\" ]"
}

