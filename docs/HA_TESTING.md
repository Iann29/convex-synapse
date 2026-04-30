# HA-per-deployment testing

This doc covers how to exercise the v0.5 HA-per-deployment path against
real infrastructure. The fast Go suite (`go test ./...`) is purely
unit/integration with the FakeDocker — sufficient for catching most
regressions but doesn't prove the lease takeover, the Postgres-backed
storage, or the S3 object-store integration end-to-end.

The real-backend test in `synapse/internal/test/ha_real_e2e_test.go` is
gated by `SYNAPSE_HA_E2E=1` and skipped in the default CI loop.

## Operator setup (one-time)

```bash
# 1. Bring up the HA profile alongside the regular stack. This adds:
#      synapse-backend-pg  — Postgres for the Convex backend's tables
#                            (separate from the synapse metadata DB)
#      synapse-minio       — S3-compatible object store
docker compose --profile ha up -d

# 2. Pre-create the buckets MinIO doesn't auto-create. Synapse expects
#    one bucket per concern: files, modules, search, exports,
#    snapshots — prefix configurable via SYNAPSE_BACKEND_S3_BUCKET_PREFIX.
docker run --rm --network synapse-network \
  -e MC_HOST_local='http://minioadmin:minioadmin@minio:9000' \
  minio/mc mb \
    local/convex-default-files \
    local/convex-default-modules \
    local/convex-default-search \
    local/convex-default-exports \
    local/convex-default-snapshots

# 3. Generate a storage key (32 bytes hex). Save somewhere safe;
#    rotating it requires re-encrypting deployment_storage rows.
openssl rand -hex 32
```

## Configuring synapse for HA mode

Add the following to your `.env` (or as `docker compose` env overrides):

```bash
SYNAPSE_HA_ENABLED=true
SYNAPSE_STORAGE_KEY=<the 64-char hex from step 3>

# Cluster-wide defaults. Each create-deployment with ha:true uses these
# as a starting point, swapping in a per-deployment database name + bucket
# names. The operator can override on a per-deployment basis through the
# create-deployment payload.
SYNAPSE_BACKEND_POSTGRES_URL=postgres://convex:convex@backend-postgres:5432/postgres?sslmode=disable
SYNAPSE_BACKEND_S3_ENDPOINT=http://minio:9000
SYNAPSE_BACKEND_S3_REGION=us-east-1
SYNAPSE_BACKEND_S3_ACCESS_KEY=minioadmin
SYNAPSE_BACKEND_S3_SECRET_KEY=minioadmin
SYNAPSE_BACKEND_S3_BUCKET_PREFIX=convex
```

Restart Synapse: `docker compose up -d synapse`.

## Smoke test from the dashboard

1. Open `http://localhost:6790`, log in, create a team + project.
2. Click **New deployment** and check **High availability (2 replicas
   + Postgres + S3)**. Submit.
3. The dashboard should show a `provisioning` row that flips to
   `running` with an `HA ×2` badge after both replicas come up.
4. `docker ps --filter label=synapse.managed=true` shows two
   `convex-{name}-0` and `convex-{name}-1` containers.

## Running the gated Go test

```bash
SYNAPSE_HA_E2E=1 \
  SYNAPSE_HA_BACKEND_POSTGRES_URL='postgres://convex:convex@localhost:5433/postgres?sslmode=disable' \
  SYNAPSE_HA_BACKEND_S3_ENDPOINT='http://localhost:9000' \
  SYNAPSE_HA_BACKEND_S3_ACCESS_KEY=minioadmin \
  SYNAPSE_HA_BACKEND_S3_SECRET_KEY=minioadmin \
  go test ./synapse/internal/test/ -run TestHA_RealBackend_Failover -count=1 -v
```

Today the test exercises the control-plane path against a real backing
Postgres. Real `Provision`-against-real-containers + `docker kill` the
active replica and assert failover lands in chunk 10 once the test
harness gains an option to inject `*dockerprov.Client` instead of the
FakeDocker stub.

## Tearing the HA profile down

```bash
docker compose --profile ha down
docker volume rm synapse_synapse-backend-pgdata synapse_synapse-minio-data
```

Plain `docker compose down` (no `--profile ha`) does **not** stop
backend-postgres or minio — they're profile-scoped and stay running
until explicitly stopped.

## Known limitations of the v0.5 release

- `upgrade_to_ha` (migrating an existing single-replica deployment to
  HA) is not yet implemented. Today HA is a create-time choice.
- The proxy picker tries replicas in `last_seen_active_at` order but
  doesn't yet run an active health probe — convergence onto the lease
  holder happens when the standby replica's first write request fails
  with `lease_lost_error` from the upstream backend, not via a
  background probe loop.
- Operator must pre-create S3 buckets (Synapse does not currently
  call `CreateBucket`). Document this in your runbook.
- Lease-takeover budget: 15-60 seconds of degraded writes during
  failover. Documented in `docs/V0_5_PLAN.md`. "HA" here means "no
  operator intervention", not "zero downtime".
