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
  `Convex.asyncSyscall("1.0/get")`. The IDv6 codec + `_tables`-backed
  mapping cache + `ConvexValue` JSON wrappers are landed; an IDv6
  string a JS bundle hands to `db.get(id)` resolves end-to-end against
  the broker's Postgres reader. Both tested in isolation.
- **What's missing for "run real Convex app":** cell-on-demand spawn
  (Synapse), Convex module loader + storage adapter (Aster). All open
  work; none is research-grade unknown.

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

## What landed in Aster (PRs #1-13 in Iann29/aster)

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
| #9 | Docs refresh — README + POSTGRES_ADAPTER_PLAN to v0.4 |
| #10 | `ASTER_STORE` env dispatch on brokerd: `memory` (default, MvccStore) vs `postgres` (PostgresCapsuleStore + ASTER_DB_URL_FILE / ASTER_DB_URL). 3-container postgres smoke harness (`docker/smoke-postgres.sh`) |
| #11 | New `crates/convex-codec/` (std-only) with `DocumentIdV6` + Crockford lowercase base32. Verbatim port of `convex-backend@main:crates/value/src/{id_v6,base32}.rs`. 11 tests (7 IDv6 + 4 base32) |
| #12 | `_tables`-backed table-mapping cache in `aster-store-postgres`. `read_point` accepts both legacy `<table_hex>/<id_hex>` and Convex IDv6 strings — IDv6 path decodes, looks up `table_number → tablet_uuid` via the `_tables` system tablet (refresh-on-miss), then runs the same SQL. Hidden / Deleting rows skipped so reused numbers can't shadow live tablets. 9 unit + 3 integration tests |
| #13 | `ConvexValue` codec in `aster-convex-codec`. `from_json` / `to_json` for the discriminated wire shape Convex apps emit (`{"$integer": ...}`, `{"$float": ...}`, `{"$bytes": ...}`). Sorted-Object semantics matching upstream's `BTreeMap`. Reserved-key rejection. 15 tests including wire-shape lock + special-float discrimination + bad-payload errors |

**Test coverage (Aster side, all green):** 56 workspace tests + 11 Postgres
integration tests + 1 cross-process E2E (brokerd + v8cell over UDS in
two real Docker containers) + 1 brokerd-postgres smoke (3 containers).

## What still doesn't work (the gap)

Today: `kind=aster` deployment exists, brokerd is up, you can create + delete
through the Synapse API + dashboard. The Aster repo can read real Convex
`documents` rows from Postgres, decode IDv6 strings + resolve them via the
`_tables` mapping cache, and the v8cell can run JS that calls
`Convex.asyncSyscall("1.0/get", ...)` and gets the document back.

What's NOT yet wired end-to-end:

1. **Cell-on-demand spawn (Synapse).** Today `kind=aster` only provisions
   the brokerd. There's no API endpoint that, given an invocation request,
   spawns a `aster-v8cell` container against the deployment's broker
   socket. The proxy's `501 aster_not_proxied` is the placeholder; this
   is where it eventually becomes "spawn a v8cell, collect stdout, return
   to caller".
2. **Convex module loader (Aster).** Today the v8cell runs an `async
   function main()` defined in a single source string. A real Convex
   module is `npx convex deploy`-bundled with `_generated/server.ts`,
   schema, multiple exports, etc. The cell needs to drive the same
   `module.<funcName>.invokeQuery(JSON.stringify(args))` shape Convex's
   own runner does (see `crates/isolate/src/environment/udf/mod.rs`
   in the upstream backend). Three sub-pieces:
     - read `_modules` to list paths + `source_package_id`
     - read `_source_packages` to resolve `storage_key`
     - pull bundle bytes from the Convex `Storage` layer (S3 or local
       FS) — this is the new code, since Aster doesn't speak storage
       yet (only `documents` reads through Postgres).

(2) is the largest remaining piece — multi-PR effort. (1) is a Synapse-
side one-shot HTTP handler that the existing PR #50 helpers already
make tractable. The minimum viable "run a real Convex app" needs both,
plus an HTTP frontend that maps `POST /api/query/<module>:<fn>` to
"spawn cell, decode args via `ConvexValue::from_json` (PR #13), invoke,
encode return via `to_json`, ship bytes back".

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
- **Aster-side:** the IDv6 codec (`crates/convex-codec/src/idv6.rs`)
  + table-mapping cache (`crates/store-postgres/src/table_mapping.rs`)
  + ConvexValue JSON wrappers (`crates/convex-codec/src/value.rs`)
  are landed. The next-largest piece is the **module loader** —
  `_modules` + `_source_packages` reads + a `Storage` adapter for the
  bundle bytes. Once the cell can pull bundled JS by module path the
  v8cell can drop its hand-written-`main()` shim.
- **Real-VPS smoke:** the `aster-e2e-fixture/` recipe walks through
  deploying a Convex app to a `kind=convex` deployment so we can
  inspect raw Postgres rows. Once cell-on-demand lands, the same
  fixture deployed to a `kind=aster` deployment is the end-to-end
  validation.
