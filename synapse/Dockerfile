# Multi-stage build for Synapse.
# Final image is distroless-style: tiny, no shell, just the binary + CA certs.

FROM golang:1.26-alpine AS builder
WORKDIR /src

# Cache deps separately from source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.Version=${VERSION}" \
    -o /out/synapse \
    ./cmd/server

# Synapse needs read/write access to the host docker socket to provision
# Convex backend containers. The socket is typically owned by root:docker —
# matching that group inside the container is host-specific and brittle, so
# we ship as a root-owned image. Treat this container as privileged: do not
# expose ports beyond 8080 to untrusted networks, and prefer to run it
# behind an authenticated reverse proxy.
FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/synapse /synapse
EXPOSE 8080
ENTRYPOINT ["/synapse"]
