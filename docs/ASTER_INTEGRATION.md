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
  brokerd container, and now **spawns a v8cell on-demand** via
  `POST /v1/deployments/{name}/aster/invoke` (#56). Operator hands JS
  source as a body field, gets stdout back. Dashboard renders an amber
  badge; the path-form proxy still returns `501 aster_not_proxied`.
  The raw-JS path was VPS-smoked with `aster-brokerd:0.4` +
  `aster-v8cell:0.4` on 2026-05-04; see `docs/ASTER_VPS_SMOKE.md`.
- **Aster:** broker reads from real Postgres + decodes IDv6 + resolves
  via `_tables` cache. The full source-package pipeline is in:
  `_modules` × `_source_packages` (#15) → on-disk `.blob` (#17, with
  the Convex `modules/<path>.js` ZIP layout corrected in #21) → IPC
  delivery to the cell (#19) → cell unzip + entry pick (#20) → **V8
  ESM compile + `<export>.invokeQuery(args_json)` dispatch with the
  existing `Convex.asyncSyscall("1.0/get", ...)` trap loop (#22)**.
  PR #22's `module_get_by_id_through_fake_broker_returns_doc` test
  runs a byte-for-byte 58 KB `npx convex deploy` bundle of the
  `aster-e2e-fixture` app's `messages.ts` through a cell and proves
  `getById` returns the seeded document end-to-end. **First-class
  proof that a real Convex bundle executes inside an Aster cell.**
- **What's missing for "real Convex app over real network":** the
  cell binary now wires the new entry (#23) and a docker-driven
  end-to-end smoke proves `getById` returns the seeded document
  through the binaries against real Postgres (#24). Remaining gaps
  are integration-shaped, not capability-shaped: real-VPS smoke that
  drives the same path through *Synapse* (today's docker smoke uses
  raw `docker run`, not Synapse's `provisionAster` + `aster/invoke`),
  a per-deployment source model that durably points Aster at a
  specific Convex deployment's Postgres / modules directory (today
  process-level via SYNAPSE_ASTER_*), and a Convex-shaped HTTP
  frontend that maps `/api/query/<module>:<fn>` to a cell invocation.

## What landed in Synapse (PRs #49-59)

| PR | What |
|---|---|
| #49 | `kind` column on `deployments` (migration 000010), API field on `create_deployment`, validation, `list_deployments` + `get_deployment` echo it |
| #50 | `Docker.Provision` branches on `spec.Kind == "aster"` → `provisionAster` (creates `synapse-aster-{name}` volume + `aster-brokerd:0.4` container). `DestroyAster` + `StatusAster` siblings. Delete-handler routes by kind. Worker carries `Kind` from the deployments row to the spec. Health worker dispatches by kind. |
| #51 | Proxy `/d/{name}/*` returns `501 aster_not_proxied` with structured JSON for kind=aster, instead of falling through to a 502 |
| #52 | Dashboard amber "aster" badge + Open-Dashboard button disabled with tooltip |
| #53 | `CLAUDE.md` updated to reference the four PRs |
| #54 | `aster-e2e-fixture/` Convex app (1 table, 1 query, 1 mutation) used to drive a future end-to-end |
| #55 | Comprehensive integration sweep — `docs/ASTER_INTEGRATION.md` consolidated, CLAUDE.md updated, README pointer landed |
| #56 | **Cell on-demand spawn endpoint** — `POST /v1/deployments/{name}/aster/invoke` takes `{js, snapshotTs, prewarm, ...}`, validates kind=aster, spawns a one-shot v8cell against the brokerd's UDS volume with `ASTER_JS_INLINE` env (requires Iann29/aster#16), waits, returns `{stdout, stderr, exitCode}`. Cell never sees Postgres credentials. 64 KiB JS cap (Docker env-var ceiling), 30s host-side timeout. |
| #57 | **Aster images 0.4 + VPS smoke** — Synapse pins `aster-brokerd:0.4` and `aster-v8cell:0.4` through `AsterImageTag`, clears `ASTER_JS=` before setting inline JS so file-based image defaults cannot collide with the mutually-exclusive source modes, and documents the raw-JS real-VPS smoke in `docs/ASTER_VPS_SMOKE.md`. |
| #58 | Docs/test cleanup after the VPS smoke: Aster runtime status docs stopped claiming cell-on-demand was pending, and flaky provisioner/dashboard waits were tightened. |
| #59 | **Aster Postgres/modules bridge config** — optional `SYNAPSE_ASTER_POSTGRES_URL`, `SYNAPSE_ASTER_DB_SCHEMA`, and `SYNAPSE_ASTER_MODULES_DIR` pass through compose/env template → config → provisioning worker → brokerd container. When configured, brokerd runs `ASTER_STORE=postgres` and gets a read-only bind mount at `/run/aster/modules`; empty config preserves the memory-store raw-JS smoke path. Requires a broker image with Iann29/aster#19 for module bundle IPC. |

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
- `TestDeployments_InvokeAsterCellHappyPath` — JSSource + InstanceSecret +
  DeploymentName flow through to docker layer; cell stdout returns to client.
- `TestDeployments_InvokeAsterCellRejectsNonAster` — kind=convex deployment
  → 409 `wrong_kind`, docker untouched.
- `TestDeployments_InvokeAsterCellRejectsEmptyJS` — empty `js` → 400
  `missing_js`, docker untouched.
- `TestDeployments_CreateAsterPassesRuntimeConfigToWorker` —
  `SYNAPSE_ASTER_*`-equivalent harness config reaches the docker spec.

## What landed in Aster (PRs #1-24 in Iann29/aster)

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
| #14 | Docs refresh to v0.5 — README + crate metadata names all three convex-codec modules + the new "module loader is the next gap" framing |
| #15 | **Module index** — `_modules` × `_source_packages` join via the table-mapping cache's new `lookup_by_name` path. New `find_module(path)` / `list_modules()` on `PostgresCapsuleStore` return a `ModuleDescriptor` with `{path, source_package_internal_id, storage_key, environment, sha256s, unzipped_size}` ready for the storage adapter (next slice). Reuses ConvexValue codec to parse the `$bytes`/`$integer`-wrapped source-package body |
| #16 | **`ASTER_JS_INLINE` on v8cell** — sibling of `ASTER_JS=<path>`; lets a caller pass JS source via env without ferrying a file across the docker-out-of-docker boundary. Required by Synapse's `aster/invoke` endpoint. Mutually exclusive with `ASTER_JS` (typed error if both set) |
| #17 | **Modules storage adapter (local FS)** — `PostgresConfig.modules_dir`; `load_module_bundle(path)` joins `find_module` + on-disk read at `<modules_dir>/<storage_key>.blob` + sha256 verify. Trait shape lets S3 land later. Inline SHA-256 (FIPS 180-4) keeps the production graph small; tests use the `sha2` dev-dep |
| #18 | Aster image metadata/docs refresh to `aster-brokerd:0.4` + `aster-v8cell:0.4`, matching the Synapse raw-JS VPS smoke. |
| #19 | **Module bundle IPC** — `LoadModuleBundle { context, capsule, path }` over UDS, base64 payload on the JSON wire, broker-side capsule/context verification before serving bytes, and `ASTER_MODULES_DIR` wired into `PostgresConfig.modules_dir`. |
| #20 | **Cell-side bundle ingestion** — v8cell binary's new `ASTER_MODULE_PATH` env (mutually exclusive with `ASTER_JS` / `ASTER_JS_INLINE`) bootstraps a sealed capsule, calls `LoadModuleBundle`, unzips the response, picks `<path>` or `<path>.js` entry. New `aster-ipc::bundle::extract_module_source` with 8 unit tests covering happy path, suffix policy, missing-entry diagnostic, non-ZIP / non-UTF-8 rejection, nested paths. |
| #21 | **Bundle entry name fix** — research against a real `npx convex deploy` (memo at `/tmp/aster-research-bundle-ground-truth.md`) showed Convex's backend re-packages the wire payload as a ZIP with entries prefixed `modules/<path>.js`. PR #20's lookup didn't include that prefix; PR #21's `candidate_names` tries `modules/<path>.js` first. +2 unit tests. |
| #22 | **V8 ESM module loader + invokeQuery dispatch (the proof point)** — new `V8SandboxCell::execute_module_query_with_broker` compiles a real `npx convex deploy` bundle as an ES module, evaluates it (top-level await pumped via `MicrotasksPolicy::Explicit`), reads the named export, asserts `isQuery === true` (rejects mutations/actions for v0.5), calls `<export>.invokeQuery(argsJson)`, drives the existing `Convex.asyncSyscall("1.0/get", ...)` trap loop while the user handler awaits. **The test `module_get_by_id_through_fake_broker_returns_doc` runs the byte-for-byte 58 KB bundle of `aster-e2e-fixture/convex/messages.ts` through the cell — `getById` returns the seeded document with name/body/_id intact and exactly 1 trap.** That test is the project's first proof that a real Convex bundle executes end-to-end inside an Aster cell. 3 integration tests + the bundled fixture file. |
| #23 | **Cell binary wires the module path** — `aster_v8cell` binary gains `ASTER_FUNCTION_NAME` + `ASTER_ARGS_JSON` envs that route to PR #22's library entry point. Mutually exclusive with `ASTER_JS` / `ASTER_JS_INLINE`; half-config rejected with operator-actionable error messages (e.g. naming the missing env, hinting `[]` for zero-arg queries). 11 unit tests on the env-parse paths via an `EnvMap` helper that avoids `std::env`'s process-global state. |
| #24 | **Real-bundle docker smoke (the END-TO-END proof)** — `docker/smoke-bundle.sh` boots `postgres:16`, stages a real `npx convex deploy` ZIP at `<modules_dir>/<storage_key>.blob` (with the bundle's actual SHA-256 in `_source_packages.sha256`), spins brokerd in `ASTER_STORE=postgres` mode, runs the v8cell binary against it. Output: `{"output":"{\"_id\":\"messages|aaaa...\",\"name\":\"ian\"}","traps":1}` — real bundle compiled as ESM, real `getById.invokeQuery` invoked, real `Convex.asyncSyscall("1.0/get")` trap drained against real Postgres, real document returned. **This proves the full path against the real binaries, not just the library.** |

**Test coverage (Aster side, all green):** 106 workspace tests (incl. 3 module-loader integration tests against a real Convex bundle + 11 binary env-parse tests in #23) + 19 Postgres
integration tests + 1 cross-process E2E (brokerd + v8cell over UDS in
two real Docker containers) + 1 brokerd-postgres smoke (3 containers).

## What still doesn't work (the gap)

Today: `kind=aster` deployment exists, brokerd is up, you can create
+ delete + **invoke handwritten JS** end-to-end through the Synapse
API. The Aster repo can read real Convex `documents` rows from
Postgres, decode IDv6 strings + resolve them via the `_tables`
mapping cache, find a deployed module's storage_key (PR #15) +
read its bundle bytes from local FS / broker IPC (PRs #17, #19), and the
v8cell can run JS that calls `Convex.asyncSyscall("1.0/get", ...)`.

What's NOT yet wired end-to-end:

1. **Cell-side module loader (Aster).** Even with bundle bytes in
   hand, the v8cell still runs a single `async function main()`
   from a string — it can't parse the upstream ZIP, pick the right
   `<path>.js` entry, set up V8 module imports / Convex shims
   (`convex/server`, `convex/values`, `_generated/api`), nor route
   `module:fn(args)` to the right export. Lands as fatia 3 of #98.
2. **Source-storage selection.** Aster #19 can serve bundle bytes over
   broker IPC, and Synapse #59 can point brokerd at a process-level
   Postgres URL + modules host path. What is still missing is a durable
   per-deployment source model: e.g. "this kind=aster deployment mirrors
   that kind=convex deployment's Postgres/modules storage" without relying
   on global operator env for all Aster deployments.
3. **HTTP frontend (`/api/query/<module>:<fn>`).** Today the only
   way in is `POST /aster/invoke` with raw JS. To accept real
   Convex CLI / client traffic, Synapse needs a request router
   that maps `<module>:<fn>(args)` to the right Aster invocation —
   parsing args via the ConvexValue codec (PR #13), spawning the
   cell with module-loader-aware envs.

(1) is the largest remaining piece — multi-PR effort. The minimum viable
"run a real Convex app" needs that loader, the module bundle plumbing in
(2), plus an HTTP frontend that maps `POST /api/query/<module>:<fn>` to
"spawn cell, decode args via `ConvexValue::from_json` (PR #13), invoke,
encode return via `to_json`, ship bytes back".

## Aster runtime bridge config

`kind=aster` keeps working with no extra config: brokerd boots the memory
store and `/aster/invoke` can run handwritten JS that does not need real
Convex rows. To point brokerd at an existing Convex Postgres/modules source,
operators can set:

```bash
SYNAPSE_ASTER_POSTGRES_URL=postgres://...
SYNAPSE_ASTER_DB_SCHEMA=public
SYNAPSE_ASTER_MODULES_DIR=/host/path/to/convex/storage/modules
```

Important details:

- `SYNAPSE_ASTER_MODULES_DIR` is a Docker-host path because Synapse talks to
  the host Docker daemon. It is mounted read-only into brokerd at
  `/run/aster/modules`.
- The path is the `modules` directory itself. Aster expects
  `/run/aster/modules/<storage_key>.blob`.
- This is a bridge knob, not the final product model. It is enough for
  fixture/VPS work, but production needs per-deployment source selection.

## Operator runbook (today)

**Spinning up a kind=aster deployment** (registers metadata + brokerd
container; raw-JS execution uses `/aster/invoke`, not the deployment proxy):

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

**Image expectations:** `aster-brokerd:0.4` and `aster-v8cell:0.4` must
exist on the host. Today they're built from the [Aster repo](https://github.com/Iann29/aster)
and copied with `docker save | scp | docker load` to the target host; the
Aster repo's CI builds and smokes the images but does not publish them to a
registry yet. The setup.sh doesn't pull them automatically yet. The
2026-05-04 VPS smoke validated both images plus the Synapse raw-JS invoke
path; the fixture-backed `Convex.asyncSyscall("1.0/get")` smoke remains
open. The first fixture-backed attempt proved the Convex fixture deploy +
control query path, then returned `output:null` from Aster because the VPS was
non-HA SQLite storage and brokerd was not configured with `ASTER_STORE=postgres`;
see `docs/ASTER_VPS_SMOKE.md`.

**Reaching the deployment over HTTP:** `/d/{name}/*` returns 501 with
`code: "aster_not_proxied"` and a structured message — that's the
expected behaviour today. The dashboard renders the amber badge and
disables Open Dashboard with a tooltip explaining the same thing.

## Where to look next

If you're picking up Aster work:

- **Synapse-side:** raw-JS cell-on-demand is in place via
  `POST /v1/deployments/{name}/aster/invoke`; the Postgres/modules bridge
  config exists via #59. The next Synapse slices are per-deployment source
  selection and the Convex-shaped `/api/query/<module>:<fn>` frontend once
  the cell-side loader exists.
- **Aster-side:** the IDv6 codec (`crates/convex-codec/src/idv6.rs`)
  + table-mapping cache (`crates/store-postgres/src/table_mapping.rs`)
  + ConvexValue JSON wrappers (`crates/convex-codec/src/value.rs`)
  are landed. The next-largest piece is the **module loader** —
  `_modules` + `_source_packages` reads + a `Storage` adapter for the
  bundle bytes. Once the cell can pull bundled JS by module path the
  v8cell can drop its hand-written-`main()` shim.
- **Real-VPS smoke:** `docs/ASTER_VPS_SMOKE.md` captures the raw-JS
  `0.4` image smoke and the first fixture-backed attempt. The
  `aster-e2e-fixture/` deploy/control-read path works, but the current VPS
  run did not seed Postgres because the test install was non-HA. The next
  end-to-end validation needs a shared Postgres-backed Convex deployment before
  invoking `Convex.asyncSyscall("1.0/get")` from the Aster cell.
