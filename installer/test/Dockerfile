# Bats test image — bats/bats:latest plus the runtime tools the
# installer scripts actually shell out to (jq for JSON parsing,
# curl for HTTP). The base image is Alpine; both packages add
# ~5 MB total and the build is fully cacheable.
FROM bats/bats:latest

RUN apk add --no-cache jq curl
