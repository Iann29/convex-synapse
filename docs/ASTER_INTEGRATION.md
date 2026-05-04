# Aster integration status

[Aster](https://github.com/Iann29/aster) is an open-source execution
plane for self-hosted Convex deployments — a capability-narrowed runner
where tenant JS executes in a V8 cell **without database credentials**,
fed sealed snapshot capsules by a broker that owns the Postgres handle.
This document is the single source of truth for which pieces are wired
to Synapse, which still live only in the Aster repo, and what's open.

The Aster repo's own design memos
([`docs/POSTGRES_ADAPTER_PLAN.md`](https://github.com/Iann29/aster/blob/main/docs/POSTGRES_ADAPTER_PLAN.md),
[`docs/CONVEX_POSTGRES_REFERENCE.md`](https://github.com/Iann29/aster/blob/main/docs/CONVEX_POSTGRES_REFERENCE.md),
the `ARCHITECTURE_V0.{1,2,3}.md` series) cover the design and the trail
of decisions; this doc focuses on **what runs through Synapse today**.

## TL;DR

- **Synapse:** kind=aster registers + provisions + delete-cleans a real
  brokerd container. The proxy returns `501 aster_not_proxied` because
  the request path isn't wired yet. Dashboard renders an amber badge.
- **Aster:** broker reads from real Postgres. V8 cell speaks
  `Convex.asyncSyscall("1.0/get")`. Both tested in isolation.
- **What's missing for "run real Convex app":** cell-on-demand spawn
  (Synapse), IDv6 ↔ DocumentId mapping (Aster), Convex module loader
  (Aster). All three are open work; none is research-grade unknown.

## What landed in Synapse (PRs #49-54)

| PR | What |
|---|---|
| #49 | `kind` column on `deployments` (migration 000010), API field on `create_deployment`, validation, `list_deployments` + `get_deployment` echo it |
| #50 | `Docker.Provision` branches on `spec.Kind == "aster"` → `provisionAster` (creates `synapse-aster-{name}` volume + `aster-brokerd:0.3` container). `DestroyAster` + `StatusAster` siblings. Delete-handler routes by kind. Worker carries `Kind` from the deployments row to the spec. Health worker dispatches by kind. |
| #51 | Proxy `/d/{name}/*` returns `501 aster_not_proxied` with structured JSON for kind=aster, instead of falling through to a 502 |
| #52 | Dashboard amber "aster" badge + Open-Dashboard button disabled with tooltip |
| #53 | `CLAUDE.md` updated to reference the four PRs |
| #54 | `aster-e2e-fixture/` Convex app (1 table, 1 query, 1 mutation) used to drive a future end-to-end |

**Test coverage (Synapse side, all green in CI):**

- `TestDeployments_CreateAsterEnqueuesProvisioning` — full pipeline:
  create → wait for status=running → assert `Provisioned[0].Kind == "aster"`,
  no host port, no Storage env.
- `TestDeployments_DeleteAsterRoutesToDestroyAster` — `DestroyAster`
  is called, `Destroy` is NOT.
- `TestDeployments_ListIncludesKind` / `TestDeployments_GetReturnsKind`
  — wire shape pinned both directions.
- `TestProxy_KindAsterReturns501NotImplemented` — typed 501 + `kind: "aster"`
  in the body.
- `TestDeployments_CreateRejectsAsterPlusHA` — combination is `400 invalid_combination`.

## What landed in Aster (PRs #1-8 in Iann29/aster)

| PR | What |
|---|---|
| #1 | Multi-stage Dockerfile (`runtime-broker`, `runtime-v8cell` targets), `docker/smoke.sh` E2E, GitHub Actions CI |
| #2 | `CapsuleStore` trait + `StoreError` enum. Generic `LocalCapsuleBroker<S>`. Blanket impls for `&MvccStore`, `MvccStore`, `Arc<S>` |
| #3 | `aster_brokerd` uses `Arc<dyn CapsuleStore + Send + Sync>` instead of concrete `MvccStore` |
| #4 | New `crates/store-postgres/` workspace member with `tokio-postgres` + `deadpool-postgres`. Stub queries, lazy connect, sync API + internal tokio runtime |
| #5 | `docs/CONVEX_POSTGRES_REFERENCE.md` — verbatim DDL + read SQL templates from `get-convex/convex-backend`, 12 gotchas |
| #6 | CI `postgres-it` lane with `postgres:16` service container |
| #7 | Real SQL: `snapshot_ts`, `read_point` (DISTINCT ON id), `read_prefix`. 8 integration tests against postgres:16 |
| #8 | `Convex.asyncSyscall("1.0/get")` JS API on the v8cell global. `PendingTrap` enum (AsterRead | ConvexSyscall). Document `_raw` bytes round-trip through `JSON.parse` on the JS side |

**Test coverage (Aster side, all green):** 28 workspace tests + 8 Postgres
integration tests + 1 cross-process E2E (brokerd + v8cell over UDS in
two real Docker containers).

## What still doesn't work (the gap)

Today: `kind=aster` deployment exists, brokerd is up, you can create + delete
through the Synapse API + dashboard. The Aster repo can read real Convex
`documents` rows from Postgres, and the v8cell can run JS that calls
`Convex.asyncSyscall("1.0/get", ...)` and gets the document back.

What's NOT yet wired end-to-end:

1. **Cell-on-demand spawn (Synapse).** Today `kind=aster` only provisions
   the brokerd. There's no API endpoint that, given an invocation request,
   spawns a `aster-v8cell` container against the deployment's broker
   socket. The proxy's `501 aster_not_proxied` is the placeholder; this
   is where it eventually becomes "spawn a v8cell, collect stdout, return
   to caller".
2. **IDv6 ↔ Aster DocumentId (Aster).** `Convex.asyncSyscall("1.0/get")`
   accepts `{id}` where `id` is a string. Today the v8cell parses it
   directly as `<table_hex>/<id_hex>` (Aster's encoding). The real
   Convex CLI hands a base32-encoded IDv6; the broker needs to decode
   it via the table mapping (`_tables` system tablet) before the SQL
   read can happen.
3. **Convex module loader (Aster).** Today the v8cell runs an `async
   function main()` defined in a single source string. A real Convex
   module is `npx convex deploy`-bundled with `_generated/server.ts`,
   schema, multiple exports, etc. The cell needs to drive the same
   `module.<funcName>.invokeQuery(JSON.stringify(args))` shape Convex's
   own runner does (see `crates/isolate/src/environment/udf/mod.rs`
   in the upstream backend).

The "Convex JS runtime research" memo from the Aster PR #8 trail
(reproduced in Aster's `docs/CONVEX_POSTGRES_REFERENCE.md` companion
notes) identifies (3) as the largest remaining piece, but (2) is the
shortest in calendar time and would let an operator manually deploy
a Convex app and test by hand — useful as a milestone before (3).

## Operator runbook (today)

**Spinning up a kind=aster deployment** (registers metadata + brokerd
container; nothing executes yet):

```bash
TOKEN=$(curl -sS -X POST "$SYNAPSE_URL/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$EMAIL\",\"password\":\"$PASS\"}" | jq -r .accessToken)

curl -sS -X POST "$SYNAPSE_URL/v1/projects/$PROJECT_ID/create_deployment" \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d '{"type":"dev","kind":"aster"}'
# → { "name":"...","kind":"aster","status":"provisioning",... }
```

Wait ~2s for the worker to flip status to `running`; the brokerd container
becomes visible as `aster-broker-<name>` in `docker ps`.

**Image expectations:** `aster-brokerd:0.3` and `aster-v8cell:0.3` must
exist on the host. Today they're built from the [Aster repo](https://github.com/Iann29/aster)
and either pushed to a registry or `docker save | scp | docker load` to the
target host. The setup.sh doesn't pull them automatically yet.

**Reaching the deployment over HTTP:** `/d/{name}/*` returns 501 with
`code: "aster_not_proxied"` and a structured message — that's the
expected behaviour today. The dashboard renders the amber badge and
disables Open Dashboard with a tooltip explaining the same thing.

## Where to look next

If you're picking up Aster work:

- **Synapse-side:** the natural next slice is the cell-on-demand
  endpoint. `synapse/internal/api/deployments.go` already has the
  `kind=aster` branch in `createDeployment`; mirror it as a
  `POST /v1/deployments/{name}/aster/invoke` that spawns
  `aster-v8cell:0.3` against the existing brokerd's volume, collects
  stdout, returns it. `internal/docker/aster.go` is where the helpers
  for the new container go.
- **Aster-side:** the `docs/CONVEX_POSTGRES_REFERENCE.md` memo lays
  out the IDv6 codec verbatim (`crates/value/src/id_v6.rs` upstream).
  The broker's table-mapping cache lives in `crates/store-postgres/src/lib.rs`;
  it currently reads `documents` directly without resolving the
  tablet UUID, which is the next correctness gap.
- **Real-VPS smoke:** the `aster-e2e-fixture/` recipe walks through
  deploying a Convex app to a `kind=convex` deployment so we can
  inspect raw Postgres rows. Once cell-on-demand lands, the same
  fixture deployed to a `kind=aster` deployment is the end-to-end
  validation.
