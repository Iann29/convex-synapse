# Quickstart

Get a Synapse stack running in about three minutes.

## Prerequisites

- Docker + Docker Compose v2
- `git` (the auto-installer git-clones itself when piped via curl)
- (Optional, for development) Go 1.22+ and Node 20+

## One-liner: hosted `curl | bash`

For a single-VPS install with TLS:

```bash
curl -sSf https://raw.githubusercontent.com/Iann29/convex-synapse/main/setup.sh \
  | bash -s -- --domain=synapse.yourdomain.com
```

Or local-only, no TLS:

```bash
curl -sSf https://raw.githubusercontent.com/Iann29/convex-synapse/main/setup.sh \
  | bash -s -- --no-tls --skip-dns-check --non-interactive
```

`bash -s -- <flags>` is how you forward arguments through a pipe. The
script auto-detects the curl|bash invocation, clones the repo into
`/tmp/convex-synapse-bootstrap-<pid>`, and re-execs from there — every
flag you passed is preserved.

That's it for production-style installs. Read on for the dev-loop
flows.

## Five-minute path: Docker Compose (manual)

If you'd rather inspect the script before running it, or you want
a hackable checkout next to your editor:

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

## Using `npx convex` with a Synapse-managed deployment

The Convex CLI's self-hosted mode looks for two env vars:
`CONVEX_SELF_HOSTED_URL` and `CONVEX_SELF_HOSTED_ADMIN_KEY`. When both are
set (and `CONVEX_DEPLOYMENT` is **not**), the CLI skips Big Brain and talks
straight to the deployment.

The thin Synapse CLI wrapper in `cli/` automates that setup while still
delegating all Convex work to the official `npx convex` package:

```bash
# From the Synapse checkout, install the local wrapper binary once.
cd cli && npm link

# In a Convex app directory, log in to Synapse and select a deployment.
cd /path/to/my-test-app
synapse login http://localhost:8080
synapse select

# Push code, run functions, deploy — all against the selected Synapse backend.
npx convex dev --once
npx convex deploy
```

`synapse select` lists your teams, projects, and deployments via Synapse's
existing `/v1` API, then writes `.env.local` with the two
`CONVEX_SELF_HOSTED_*` values. If the file already has `CONVEX_DEPLOYMENT`,
the wrapper comments it out so the official CLI doesn't enter the Cloud/Big
Brain path by accident. You can also use `synapse convex dev` as a direct
delegating shortcut; it runs `npx convex dev` with conflicting shell env vars
removed.

Full end-to-end:

```bash
# 1. (Already done above) Provision a deployment via Synapse
NAME=$(curl -sf http://localhost:8080/v1/projects/$PID/deployment \
        -H "Authorization: Bearer $A" \
       | python3 -c "import sys,json; print(json.load(sys.stdin)['name'])")

# 2. Bootstrap a sample app
mkdir my-test-app && cd my-test-app
npx create-convex@latest .

# 3. Let the wrapper write .env.local for the deployment you choose
synapse login http://localhost:8080
synapse select
npx convex dev --once
```

Synapse still exposes the raw env-var pair on a single endpoint for scripts
or CI flows that do not want the wrapper:

```bash
eval "$(curl -sf http://localhost:8080/v1/deployments/<NAME>/cli_credentials \
        -H "Authorization: Bearer $A" \
      | python3 -c 'import sys,json; print(json.load(sys.stdin)["exportSnippet"])')"
```

A `<CliCredentialsPanel>` React component
(`dashboard/components/CliCredentialsPanel.tsx`) renders the same snippet
inline with a one-click copy button — drop it next to a deployment row and
the user gets the export lines without ever touching `curl`.

## Tearing it down

```bash
docker compose down              # stop services, keep data volumes
docker compose down -v           # also drop the metadata DB
docker rm -f $(docker ps -aq --filter label=synapse.managed=true)
docker volume ls -q --filter name=synapse-data- | xargs -r docker volume rm
```

The last two lines clean up provisioned Convex backends — `compose down -v`
only touches the synapse-* services.
