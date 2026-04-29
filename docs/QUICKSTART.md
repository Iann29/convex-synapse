# Quickstart

> **Status:** placeholder. This page will be the main onboarding doc once v0.1 lands. The commands below are what we're building toward — they don't all work yet.

## Prerequisites

- Docker + Docker Compose
- (Dev only) Go 1.22+ if running Synapse outside Docker

## 5-minute setup

```bash
git clone <this-repo> synapse && cd synapse
cp .env.example .env   # tweak SYNAPSE_JWT_SECRET, postgres password
docker compose up -d
```

Open `http://localhost:6790` and create your first user. You'll land on a team home page. Click **New project**, then **New deployment** → and Synapse spins up a fresh Convex backend container in seconds.

## What just happened

Three containers are running:

```
$ docker compose ps
NAME                IMAGE                                       STATUS    PORTS
synapse-postgres    postgres:16-alpine                          running   5432
synapse-api         synapse:latest                              running   8080
synapse-dashboard   synapse-dashboard:latest                    running   6790
```

Plus one container per deployment you provisioned:

```
$ docker ps --filter "label=synapse.managed=true"
NAME                          IMAGE                                       PORTS
convex-quiet-cat-1234         ghcr.io/get-convex/convex-backend:latest    3210
convex-fast-rabbit-5678       ghcr.io/get-convex/convex-backend:latest    3211
```

## Connecting `npx convex`

```bash
npx convex login --url http://localhost:8080
npx convex dev --deployment-name quiet-cat-1234
```

(post-v0.2 — for now use the deployment URL + admin key directly)

## Uninstall

```bash
docker compose down -v   # nukes postgres data and provisioned deployments
```
