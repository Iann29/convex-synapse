# Quickstart

Get a Synapse stack running in about three minutes.

## Prerequisites

- Docker + Docker Compose v2
- (Optional, for development) Go 1.22+ and Node 20+

## Five-minute path: Docker Compose

```bash
git clone https://github.com/Iann29/convex-synapse.git
cd convex-synapse
cp .env.example .env

# Generate a secure JWT secret in place of the placeholder
echo "SYNAPSE_JWT_SECRET=$(openssl rand -hex 64)" >> .env

docker compose up -d
```

Three containers come up:

```
$ docker compose ps
NAME                  IMAGE                       STATUS    PORTS
synapse-postgres      postgres:16-alpine          running   5432
synapse-api           synapse:dev                 running   8080
synapse-dashboard     synapse-dashboard:dev       running   6790
```

Open `http://localhost:6790`, register your first user, and you'll land on a
team listing. Click **New project** → **New deployment** → and Synapse spins
up a fresh Convex backend container in seconds.

## Manual / dev path (Synapse on host, postgres in Docker)

```bash
git clone https://github.com/Iann29/convex-synapse.git
cd convex-synapse
cp .env.example .env

# Edit .env to point synapse at localhost postgres
sed -i 's|@postgres:5432|@localhost:5432|' .env

# Bring up postgres only
docker compose up -d postgres

# Build & run synapse
cd synapse
go run ./cmd/server
# → JSON logs scroll on stdout; the API listens on :8080
```

In another shell:

```bash
cd dashboard
npm install
NEXT_PUBLIC_SYNAPSE_URL=http://localhost:8080 npm run dev
# → http://localhost:3000
```

## Verifying with curl

```bash
# Register
A=$(curl -sf -X POST http://localhost:8080/v1/auth/register \
      -H 'Content-Type: application/json' \
      -d '{"email":"you@example.com","password":"strongpass123"}' \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['accessToken'])")

# Create team + project
curl -sf -X POST http://localhost:8080/v1/teams/create_team \
  -H "Authorization: Bearer $A" -H 'Content-Type: application/json' \
  -d '{"name":"My Team"}'
curl -sf -X POST http://localhost:8080/v1/teams/my-team/create_project \
  -H "Authorization: Bearer $A" -H 'Content-Type: application/json' \
  -d '{"projectName":"My Project"}'

# Provision a real Convex backend (returns once container is healthy)
PID=$(curl -sf http://localhost:8080/v1/teams/my-team/list_projects \
        -H "Authorization: Bearer $A" \
      | python3 -c "import sys,json; print(json.load(sys.stdin)[0]['id'])")
curl -sf -X POST http://localhost:8080/v1/projects/$PID/create_deployment \
  -H "Authorization: Bearer $A" -H 'Content-Type: application/json' \
  -d '{"type":"dev","isDefault":true}'

# A new container appears
docker ps --filter label=synapse.managed=true
```

## Pointing `npx convex` at a Synapse-managed deployment

After `create_deployment`, the response includes a `name` and the dashboard's
**Open** button knows the URL + admin key. To use the Convex CLI:

```bash
# Get the credentials
curl -sf http://localhost:8080/v1/deployments/<NAME>/auth \
  -H "Authorization: Bearer $A"
# → {deploymentName, deploymentUrl, adminKey, deploymentType}

# Point the CLI at it
export CONVEX_URL=<deploymentUrl>
export CONVEX_DEPLOY_KEY=<adminKey>     # for now — proper deploy keys are v0.2
npx convex dev
```

## Tearing it down

```bash
docker compose down              # stop services, keep data volumes
docker compose down -v           # also drop the metadata DB
docker rm -f $(docker ps -aq --filter label=synapse.managed=true)
docker volume ls -q --filter name=synapse-data- | xargs -r docker volume rm
```

The last two lines clean up provisioned Convex backends — `compose down -v`
only touches the synapse-* services.
