---
name: synapse-dev
description: Spin up the Synapse local dev stack (postgres + Go backend), reset the database, run smoke tests against the API. Use when the user asks to "run synapse", "start the backend", "reset the db", "rebuild and run", or wants to manually exercise endpoints.
---

# Synapse dev workflow

Synapse is the Go control-plane backend in this repo. Its local dev stack is:

- **Postgres** in Docker (`docker-compose.yml` service `postgres`, port 5432)
- **Go backend** running on the host on port 8080 (loads `../.env`)

## Start the stack

```bash
# From repo root
docker compose up -d postgres

# Synapse in foreground (loads ../.env)
cd synapse
go run ./cmd/server
```

If `go run` errors with `SYNAPSE_JWT_SECRET must be set`, ensure `.env` exists
in the repo root: `cp .env.example .env`.

## Build & vet (use before every commit)

```bash
cd synapse
go build ./... && go vet ./...
```

## Reset the database (keep schema, drop data)

```bash
PGPASSWORD=synapse psql -h localhost -U synapse -d synapse -c \
  "TRUNCATE users, teams, projects, team_members, deployments, project_env_vars, \
   team_invites, deploy_keys, access_tokens, audit_events RESTART IDENTITY;"
```

Plain `DELETE FROM users` will fail because `teams.creator_user_id` has
`ON DELETE RESTRICT`. Either delete teams first or `TRUNCATE … RESTART IDENTITY`.

## Smoke test the API

After running the server, the canonical e2e flow is:

```bash
A=$(curl -sf -X POST http://localhost:8080/v1/auth/register \
      -H 'Content-Type: application/json' \
      -d '{"email":"dev@local","password":"strongpass123","name":"Dev"}' \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['accessToken'])")

curl -sf -X POST http://localhost:8080/v1/teams/create_team \
  -H "Authorization: Bearer $A" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Test Team"}'
```

The token is a JWT — paste into `Authorization: Bearer <token>` for any
authenticated endpoint.

## Common pitfalls

- **Working directory matters** for the `.env` loader. The server tries `.env`,
  `../.env`, `../../.env` in that order. Run from `synapse/` and the repo-root
  `.env` will be picked up via `../.env`.
- **`docker compose ps`** shows only services in the current `name:` (project
  name in compose v2 → `synapse`). If postgres is missing, you're in the wrong
  directory or the project name was overridden.
- **JWT signing** uses `SYNAPSE_JWT_SECRET`. Changing it invalidates every
  outstanding session.

## When NOT to use this skill

- Production deploy decisions — see `docs/ARCHITECTURE.md`.
- Editing the dashboard fork (separate workflow once it lands).
- Anything inside the provisioner that requires a live Docker daemon —
  the smoke flow does not exercise `Provision`/`Destroy`. Use real
  Convex backend image tests for that.
