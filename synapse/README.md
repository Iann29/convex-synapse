# synapse/

The control-plane backend. Go service that exposes the Convex management API and provisions Convex backend containers via Docker.

## Layout

```
cmd/server/main.go         — entrypoint
internal/
  api/                     — HTTP handlers (one file per resource)
  auth/                    — JWT, password hashing, token validation
  config/                  — env loader
  db/                      — pgx pool, query helpers
  docker/                  — Docker client wrapper, provisioner
  middleware/              — chi middleware (auth, logging, recover)
  models/                  — domain types
migrations/                — golang-migrate SQL files
```

## Running locally

```bash
cd synapse
cp ../.env.example ../.env
docker compose -f ../docker-compose.yml up -d postgres
go run ./cmd/server
```

## Tests

```bash
go test ./...
```

## Build

```bash
go build -o bin/synapse ./cmd/server
```
