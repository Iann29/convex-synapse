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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/synapse /synapse
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/synapse"]
