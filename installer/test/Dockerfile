# Bats test image — bats/bats:latest plus the runtime tools the
# installer scripts actually shell out to (jq for JSON parsing,
# curl for HTTP), plus python3 for the synapse-updater daemon
# end-to-end smoke (installer/test/install/daemon_smoke.bats). The
# base image is Alpine; the three packages add ~50 MB total and the
# build is fully cacheable.
FROM bats/bats:latest

RUN apk add --no-cache jq curl python3
