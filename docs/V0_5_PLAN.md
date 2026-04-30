# v0.5 — HA-per-deployment plan

> **Status:** v0.5 is now landed (10/10 chunks merged into `main`). This
> document is the original scoping pass that drove the implementation;
> it's preserved for the design rationale (lease semantics, schema
> shape, picker strategy, failover sequence). For the operator-facing
> "how do I run this?" walkthrough see [HA_TESTING.md](HA_TESTING.md).
> For the chunk-by-chunk landing log see [ROADMAP.md](ROADMAP.md).
>
> The mechanical pieces deferred to **v0.5.1** are listed at the bottom
> of [ROADMAP.md](ROADMAP.md): the `upgrade_to_ha` worker, the
> real-backend `docker kill` failover test, and the active health
> probe in the proxy.

Scoping document for the v0.5 milestone (active-passive failover per
deployment). Authored by an investigation pass.

The v0.3 multi-node-hygiene work made the **control plane** survivable to a
Synapse process restart; v0.5 makes the **data plane** survivable to a
single backend container/host failure by running active-passive replicas
behind a load balancer. The Convex backend's lease (in
`crates/postgres/src/lib.rs:1738-1810` of `get-convex/convex-backend`) is
timestamp-based: `acquire` bumps a `leases` row to the current ns
timestamp, every write is gated by `WHERE ts = my_ts`, and a higher-ts
acquire silently steals the lease — the loser shuts itself down on the
next write. We don't have to coordinate failover; we only have to (1)
keep a warm spare alive and (2) point traffic at whoever wins.

## Schema changes

Two new tables, plus tiny additive columns on `deployments`:

- `deployment_storage` — one row per deployment, holds the persistence
  backend choice and connection material:
  - `deployment_id PK FK`,
  - `db_kind ('sqlite'|'postgres')`,
  - `db_url_enc bytea` (AES-GCM, key from `SYNAPSE_STORAGE_KEY`),
  - `db_schema text` (one Postgres database/schema per deployment —
    multi-tenant inside one cluster),
  - `s3_endpoint`, `s3_region`,
  - `s3_access_key_enc`, `s3_secret_key_enc`,
  - `s3_bucket_files`, `s3_bucket_modules`, `s3_bucket_search`,
    `s3_bucket_exports`, `s3_bucket_snapshots`,
  - `created_at`.
  Synapse never reads these fields outside the provisioner / handlers
  that need them.
- `deployment_replicas` — one row per running container:
  - `id UUID PK`, `deployment_id FK`, `replica_index smallint` (0/1),
    `container_id`, `host_port int UNIQUE NULL` (null in proxy mode),
    `status ('provisioning'|'running'|'stopped'|'failed')`,
    `last_seen_active_at timestamptz NULL` (set by the LB health probe
    when it last saw 200 from this replica's lease holder),
    `created_at`. UNIQUE `(deployment_id, replica_index)`.
- `deployments` adds `replica_count smallint NOT NULL DEFAULT 1` and
  `ha_enabled bool NOT NULL DEFAULT false`. Drop the `UNIQUE` on
  `host_port` (it now lives on `deployment_replicas`); keep
  `host_port`/`container_id` columns nullable for rollback compatibility.

The `provisioning_jobs.kind` CHECK widens to include `'replica_provision'`,
`'replica_destroy'`, and `'upgrade_to_ha'`. Add a nullable `replica_id`
column for replica-level targeting; existing `'provision'` jobs stay
deployment-targeted.

## Provisioner changes

`docker.Provision(spec) -> info` becomes
`docker.ProvisionReplica(replicaSpec) -> info`. The `DeploymentSpec` grows:
`ReplicaIndex`, `DBKind`, `PostgresURL`, `S3Endpoint`, `S3Region`,
`S3Bucket*`, `S3Key`, `S3Secret`, `DoNotRequireSSL`. Container naming
becomes `convex-{deployment}-{replicaIndex}`; volume naming stays for
SQLite, becomes a no-op for Postgres-backed deployments (Postgres + S3
hold all state, no per-host volume).

`create_deployment` flow:

1. Allocate `replica_count` ports (typically 2) inside one
   `WithRetryOnUniqueViolation` block — same pattern as today, just N at
   a time.
2. Insert `deployments` + N `deployment_replicas` rows + N
   `provisioning_jobs` (`kind='replica_provision'`) atomically.
3. The `provisioner.Worker` pulls the rows in parallel — `SELECT FOR
   UPDATE SKIP LOCKED` already shards correctly — and starts both
   replicas. Both race to acquire the lease; one wins, the other parks
   in lease-acquire-loop.

Single-replica deployments keep working: `replica_count=1`,
`ha_enabled=false`, no LB indirection. Control flow is identical to today.

## Backend container args

The upstream image already supports `POSTGRES_URL`, `S3_ENDPOINT_URL`,
`AWS_ACCESS_KEY_ID/SECRET_ACCESS_KEY`,
`S3_STORAGE_{FILES,MODULES,SEARCH,EXPORTS,SNAPSHOT_IMPORTS}_BUCKET`,
`DO_NOT_REQUIRE_SSL` — no CLI flag changes, just env. `POSTGRES_URL` set
⇒ Postgres path; absent ⇒ SQLite.

**Where they come from:** cluster-wide defaults in Synapse env
(`SYNAPSE_BACKEND_POSTGRES_HOST`, `SYNAPSE_BACKEND_S3_*`) populate the
`deployment_storage` row at create-time. Per-deployment overrides via
the create-deployment payload (`{ ha: true, postgresUrl?, s3?: {...} }`)
override the cluster default. The connection string passed to each
replica gets a deployment-specific suffix: Synapse opens its own admin
connection at deployment-create-time, runs `CREATE DATABASE
convex_{name}` (or `CREATE SCHEMA` in shared-DB mode), and the
per-replica `POSTGRES_URL` points at that database/schema. Same story
for buckets — one bucket per deployment is the cleanest tenancy
boundary; nesting under a key prefix is the alternative, deferred.

Secrets stored in `deployment_storage` are encrypted at rest with
AES-GCM keyed by `SYNAPSE_STORAGE_KEY` (envelope encryption, single-
tenant operator). They never leave Synapse except as env vars on the
container we created.

## Load balancer

**Pick: Synapse-internal Go reverse proxy, extending `internal/proxy/`.**

We already mount `/d/{name}/*`. The change: `Resolver.Resolve(name)`
returns a `[]string` of replica addresses (`running` rows from
`deployment_replicas`), and the proxy uses an active-replica picker
with sticky preference — first try the replica we last saw 2xx on, fall
back to the other on connection error / 503. We do **not** round-robin
(writes against the standby are guaranteed to fail with
`lease_lost_error`).

A second goroutine runs an LB health probe (every 2s) that hits
`/api/check_admin_key` on each replica (this exercises the lease, unlike
`/version`). The picker prefers replicas with a recent successful probe.
This becomes the source of truth for `deployment_replicas.last_seen_active_at`.

Why not Caddy / HAProxy: (a) we already have a working proxy; (b) zero
new processes / config-reload story; (c) failover decision lives next
to the SQL we already read; (d) one fewer container in the compose
stack. Caddy is the right answer for v1.0 (custom domains + auto-TLS),
but for v0.5 the in-process proxy is enough.

In **host-port mode** (proxy disabled), the operator brings their own
LB; ship a 5-line Caddyfile snippet in QUICKSTART.md.

## Failover sequence

```
t=0ms     Active replica (R0) crashes (OOM / host reboot / SIGKILL)
t=0-2s    LB probe loop notices R0 health failures
t=2s      Picker promotes R1 to first-choice; new requests routed there
          R1 already running but not lease-holder → first write returns
          a transient error from upstream
t=2-15s   R1's existing lease-acquire-loop ticks (upstream's interval),
          successfully bumps the lease ts, becomes the writer
t=15-45s  R1 cold-rebuilds in-memory indexes (search, vector) — queries
          may be slow but reads work. Writes resume as soon as lease held.
t=30s     Health worker sweep notices R0 container exited, marks the
          replica row 'stopped'. With AutoRestart=true, attempts one
          restart; on success R0 rejoins as warm standby.
```

The health worker becomes **replica-aware**: it iterates
`deployment_replicas` instead of `deployments`. It does **not** make
lease decisions — those are upstream-driven. It only (a) reflects
container state into the replica row, (b) optionally restarts dead
replicas, (c) promotes a deployment to `status='failed'` only when
**all** replicas are stopped/failed.

Realistic failover budget: **15–60 seconds of degraded writes**,
depending on the upstream lease acquire interval and index size.
"HA" here means "no operator intervention", not "zero downtime".

## Out of scope for v0.5

- Multi-region (replicas land on whatever Docker daemon Synapse is
  talking to today; no cross-host placement)
- Auto-provisioning Postgres or S3 — operator must point Synapse at an
  existing managed Postgres + S3 (or MinIO) and the deployment-storage
  row records the address. No `docker run postgres` from inside Synapse.
- Per-tenant encryption keys (single `SYNAPSE_STORAGE_KEY` envelope;
  rotate via re-encrypt script later)
- Read scaling (the lease design forbids active-active writes; serving
  stale reads from the passive is upstream-supported but adds picker
  complexity)
- Custom-domain TLS termination on the LB (deferred to v1.0 with Caddy)
- More than 2 replicas (schema allows it, picker assumes 2; trivial
  extension)

## Migration story

**HA is set at create-time, with a one-shot upgrade path.**

Rationale: SQLite → Postgres requires `npx convex export` from the live
deployment + import into the new replicas (upstream's documented
procedure). That's a real outage window, not a transparent flip — make
it explicit:

- New endpoint `POST /v1/deployments/{name}/upgrade_to_ha` — accepts
  the same `{postgresUrl?, s3?}` payload as create. Body of work:
  1. Run `convex export` against the existing replica via admin key,
     stream to a tmp blob
  2. Provision two new replicas with `db=postgres`, point at fresh
     DB+buckets
  3. Run `convex import` into the new active
  4. Atomically swap: set `ha_enabled=true`, flip `replica_count`,
     mark old replica `stopped` (keep it 24h for rollback), invalidate
     proxy cache
  5. Audit-log the whole thing
- Synchronous job (queue kind `'upgrade_to_ha'`). Failure rolls back to
  the SQLite replica; the new resources get reaped.
- Existing deployments stay 1-replica + SQLite forever unless an
  operator opts in. No automatic upgrade.

Net: HA-at-create is the happy path; HA-upgrade is a documented
procedure with an outage window.

## Testing strategy

Three layers, pragmatic:

1. **Go unit-ish (FakeDocker)** — extend `synapsetest` to model
   two-replica state. `FakeDocker.Provision` pretends replicas come up;
   tests assert against `deployment_replicas` rows, the replica-aware
   health worker, and the proxy picker (driven by stuffing replica
   statuses into the DB). Covers: provisioning two replicas,
   single-replica fallback path, picker prefers healthy replica, health
   worker correctly marks a replica stopped without flipping the
   deployment, upgrade-to-ha state machine. Fast (~1s extra).
2. **Real-backend integration test** (one file, gated by
   `SYNAPSE_HA_E2E=1`): spins up real Postgres + MinIO + 2 real Convex
   backend containers via the existing compose-test pattern. Tests:
   - both replicas come up, exactly one holds the lease
     (`/api/check_admin_key` succeeds on one, the other returns lease
     error)
   - `docker kill` the active; within 60s, requests succeed via the
     standby
   - `docker start` the dead one; it parks as standby

   Single test file, ~3 min wall time. Run in CI but in a dedicated job
   that the rest of the suite doesn't depend on (so the green check on
   every PR doesn't wait on it).
3. **Playwright** — one new spec: provision an HA deployment via the
   UI, kill the active container with `docker kill` from the helper,
   refresh the dashboard, verify it still loads data. Covers the
   operator-facing path; reuses existing helpers.

Skip: chaos testing, network partition simulation, multi-failure
scenarios. Document them as known-not-covered.

## Effort estimate

Operator works part-time at ~3-4 focused hours/day. Numbers assume
existing infrastructure for Postgres / S3 connectivity.

| Chunk | Days |
|---|---|
| Schema + models + migrations + encrypted-secrets helper | 2 |
| Provisioner refactor: `ProvisionReplica` + per-deployment Postgres DB creation | 3 |
| Cluster-wide config + per-deployment override plumbing through create_deployment | 1 |
| Replica-aware health worker (iterate replicas, partial-failure logic) | 2 |
| Proxy picker: multi-address resolve, health probe loop, sticky preference | 2 |
| HA-create UI (toggle + Postgres/S3 fields with cluster defaults) | 2 |
| Go integration tests (FakeDocker layer) | 2 |
| Real-backend e2e test + CI job | 2 |
| Upgrade-to-HA endpoint + worker + Playwright happy-path | 3 |
| Docs (DESIGN.md update, QUICKSTART.md HA section, lease-takeover characteristics) | 1 |
| **Total** | **~20 days** |

At 3-4h/day part-time, plan **6-8 calendar weeks** end-to-end, with
checkpointable PRs every 2-3 chunks (schema + provisioner together;
proxy + health together; upgrade-to-HA last). Land single-replica-still-
works behind a feature flag (`SYNAPSE_HA_ENABLED`, default off) for the
first 4 chunks so `main` stays releasable.

## Key file paths

- `/home/ian/convex-2/docs/ROADMAP.md` — v0.5 contract (4 bullet points)
- `/home/ian/convex-2/synapse/internal/docker/provisioner.go:80-159` —
  current `Provision`, replaces with `ProvisionReplica`
- `/home/ian/convex-2/synapse/internal/health/worker.go:122-160` —
  sweep loop, becomes replica-aware
- `/home/ian/convex-2/synapse/internal/proxy/proxy.go` — `Resolver`,
  becomes multi-address with active-replica picker
- `/home/ian/convex-2/synapse/internal/api/deployments.go:264-345` —
  `create_deployment`, allocates N replicas
- `/home/ian/convex-2/synapse/internal/db/migrations/000001_init.up.sql:112-131` —
  `deployments` table; new migration adds `deployment_replicas` and
  `deployment_storage`
- `/home/ian/convex-2/synapse/internal/db/migrations/000002_provisioning_jobs.up.sql:18` —
  `kind` CHECK widens to `'replica_provision'`, `'replica_destroy'`,
  `'upgrade_to_ha'`
- Upstream lease: `crates/postgres/src/lib.rs:1738-1810` of
  `get-convex/convex-backend`
- Upstream env vars: `self-hosted/docker/docker-compose.yml` of
  `get-convex/convex-backend`
