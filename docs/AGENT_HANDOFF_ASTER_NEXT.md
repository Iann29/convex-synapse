# Aster integration — agent handoff

> **Mission.** Drive the Aster execution plane to the point where a real
> Convex application — bundled with `npx convex deploy` against a Convex
> backend — can be invoked through Synapse's API and have its functions
> execute inside an Aster v8cell against the same Postgres database the
> backend wrote to.
>
> **You are picking this up mid-stream.** A lot is already in place. Most
> of your time will go into a tightly-scoped pair of tasks (cell-side
> module loader + a real-VPS smoke). Read everything in the
> *Investigation* section before writing a single line of code.
>
> **Verification.** A second agent (the one writing this document) will
> review your work after each PR. Every task here ends with explicit
> *Acceptance criteria*; meet those, not just "it compiles". When a task
> says "investigate first", produce written notes (in your scratchpad or
> a comment in the PR description) before you start coding. Your
> verifier wants to see the reasoning.

---

## 1. Where things stand today

You have two repositories:

| Repo | Path on disk | What it owns |
|---|---|---|
| `Iann29/aster` | `/home/ian/aster-runner` | The Rust execution plane: brokerd, v8cell, capsule store, codecs |
| `Iann29/convex-synapse` | `/home/ian/convex-2` | The Go control plane (this repo): deployments, dashboard, proxy |

Both have green `main` branches. Run the test suites first to confirm —
if anything is red on `main` before you start, that is a bug, fix it,
do not pile new work on top of a broken baseline.

The single most important orientation document is
**`docs/ASTER_INTEGRATION.md`** in this repo. It lists every PR that has
landed on each side, what it did, and where the open gaps are.
Read it end-to-end before the rest of this document. It contains the
links you need; this handoff does not duplicate them.

When in doubt about *what is already implemented*, read the code, not
this document. Source is authoritative; this doc is a map.

---

## 2. The big picture (the path you are completing)

The end-to-end story we are building, broken into the layers it crosses:

1. **Operator deploys their Convex app** against a Convex backend
   (Synapse-managed deployment of `kind=convex` or a separate stack).
   `npx convex deploy` writes module rows into Postgres + uploads
   bundle ZIP files into the backend's local-FS storage layer.

2. **A new `kind=aster` deployment** points at the *same* Postgres +
   the *same* modules storage directory. The brokerd has the read
   handle; nobody else does.

3. **Operator (or their app) calls a function.** Today this is
   `POST /v1/deployments/{name}/aster/invoke` with raw JS in the
   body. The end-state is a Convex-shaped HTTP endpoint
   (`POST /api/query/<module>:<fn>`) that maps to the same machinery.

4. **Synapse spawns a v8cell container** against the deployment's
   brokerd. The cell mounts the broker's UDS volume read-write and
   the modules-storage directory read-only.

5. **The cell loads the requested module:** asks the brokerd for
   bundle bytes for `<path>`, unzips, picks `<path>.js` out of the
   archive, compiles it as a V8 ESM module with the Convex shims
   (`convex/server`, `convex/values`, `_generated/api`, etc.) wired
   in.

6. **The cell invokes the right export** — `module.<funcName>.invokeQuery(<argsJson>)`
   — exactly as Convex's own runner does.

7. **`ctx.db.get(id)` from inside that function** fires
   `Convex.asyncSyscall("1.0/get", ...)`. The cell traps, hands the
   IDv6 to the broker, the broker decodes via the table-mapping cache,
   reads Postgres, ships bytes back. The cell resumes the awaited
   promise.

8. **Cell prints a JSON envelope on stdout, exits.** Synapse collects
   it, ships it back as the HTTP response.

What is **already wired** (verify with `git log` on each repo):

- Layers 1, 2, 4, 7, 8 are done.
- Layers 3a (raw-JS endpoint) and 5a (broker can answer "give me bytes
  for path") in pieces — see *gaps* below.
- Layer 5b (cell-side unzip + ESM compile + shims) is **not** done.
  It is the largest open piece.
- Layer 6 (function-call routing inside the cell) is **not** done.
- Layer 3b (Convex-shaped HTTP frontend) is **not** done.

What is **partially wired** and may trip you up:

- The broker can compute "give me the bundle bytes for path X" via
  `PostgresCapsuleStore::load_module_bundle`, but **brokerd's IPC
  surface does not yet expose this to cells**. Today cells only have
  `HydratePoint`/`InitialCapsule`/`Shutdown`. Adding a
  `LoadModuleBundle` IPC variant is part of your work.
- Synapse now pins `aster-brokerd:0.4` + `aster-v8cell:0.4` through
  `AsterImageTag`. The images were rebuilt and VPS-smoked on
  2026-05-04; see `docs/ASTER_VPS_SMOKE.md`. There is still no
  registry publish workflow in `Iann29/aster`, so the operator path
  remains `docker save | scp | docker load`.

---

## 3. Conventions and ground rules

Before any task:

- **Branches.** Always branch off `main` of the relevant repo. Name
  branches `feat/<scope>-<short>` for features,
  `fix/<scope>-<short>` for fixes, `docs/<short>` for docs-only.
- **Commit style.** Follow the existing pattern (look at
  `git log --oneline` on each repo — Conventional Commits with the
  scope in parentheses). Bodies should explain *why*, not what.
  Re-read the past 10 commits before writing yours; if your style
  is visibly different, change it.
- **PR style.** Title under 70 chars. Body has a Summary, a "What's
  in / what's out" table, a Tests section, and a Test plan checklist.
  Look at any of PRs #15-17 in `Iann29/aster` or PR #56 in
  `Iann29/convex-synapse` for the shape.
- **CI gates.** On the Aster side: `cargo fmt --all -- --check`,
  `cargo build --workspace --locked`, `cargo test --workspace --locked`,
  the postgres-it lane, `aster_bench`, the docker smoke. On the
  Synapse side: `go vet ./...`, `go build ./...`, `go test ./...`,
  the bats lane (if you touch installer), the dashboard build (if
  you touch dashboard), Playwright (if you touch UI).
- **Tests are not optional.** Every PR has either new tests or a
  written justification for why the existing tests cover it. The
  verifier will reject PRs whose tests don't exercise the new code
  path against the failure mode it claims to handle.
- **Comments policy.** Comments are reserved for the *non-obvious
  why*. Do not narrate the code. Look at the existing modules —
  `module_index.rs`, `modules_storage.rs`, `aster.go` —
  for the density and tone the project expects. If your prose
  reads like a junior tutorial, cut it down.
- **Do not amend merged commits.** Only your own in-progress branch.
  Never `--force` against `main`.
- **No new top-level dependencies without a stated reason.** The
  cell-side crates explicitly avoid heavy crates so the trust
  boundary stays small. When in doubt, prefer hand-rolling something
  small over pulling a crate. (Look at the inline SHA-256 in
  `modules_storage.rs` for an example of the bar.)

When you are unsure about scope, **ask the verifier** before writing
hundreds of lines. Producing a short design memo that gets bounced is
much cheaper than a 2k-line PR that has to be redone.

---

## 4. Investigation tasks (do these first, before any coding)

These are research tasks. The output of each is *written notes* — in
the PR description that eventually carries the work, or in a scratch
file you'll delete later. The verifier will ask to see them.

### 4.1 Read the code that already shipped

Walk through, in this order:

1. `crates/store-postgres/src/lib.rs` — top-level façade. Look at
   `PostgresCapsuleStore`'s public methods to understand what the
   broker can do today (`snapshot_ts`, `read_point`, `read_prefix`,
   `find_module`, `list_modules`, `load_module_bundle`).
2. `crates/store-postgres/src/table_mapping.rs` — the `_tables`
   cache. Pay attention to `lookup_by_name` (used by the module
   index) and the refresh-on-miss pattern. Your IPC additions will
   want the same shape.
3. `crates/store-postgres/src/module_index.rs` — `_modules` and
   `_source_packages` join. Note that the `ModuleDescriptor`
   already carries everything the storage adapter needs.
4. `crates/store-postgres/src/modules_storage.rs` — the file-system
   adapter. Note especially: the inline SHA-256, the
   `path_for(<base>, <key>)` shape, the missing-file error message.
5. `crates/ipc/src/bin/aster_brokerd.rs` — the brokerd binary.
   Look at `BrokerConfig::from_env` (env-var contract) and
   `handle_request` (existing IPC dispatch). Your new IPC variant
   plugs in here.
6. `crates/ipc/src/bin/aster_v8cell.rs` — the v8cell binary. Look at
   `CellConfig::from_env` and the `SourceLocation` enum from PR #16.
   This is the piece that will need a fundamentally different "load
   module from broker" path instead of `ASTER_JS{,_INLINE}`.
7. `crates/v8cell/src/lib.rs` — the V8 host. Read all of it; the
   `PendingTrap` enum and the trap dispatch are where module-loader
   awareness has to land.
8. `crates/ipc/src/lib.rs` — IPC framing + types. New IPC variants
   live here.
9. Synapse: `synapse/internal/api/deployments.go::invokeAsterCell`
   (the new endpoint), `synapse/internal/docker/aster.go::InvokeAsterCell`
   (the docker layer), `synapse/internal/test/deployments_aster_test.go`
   (the test pattern).

After each file, write down: *one sentence about what this owns* and
*one sentence about the seam where new work plugs in*. If you can't
articulate the seam, you haven't read it carefully enough.

### 4.2 Read upstream Convex (the reference)

We have a clone at `/tmp/convex-backend`. **You will keep referring to
it.** Before starting any of the cell-side work, scan:

1. `crates/value/src/id_v6.rs` and `crates/value/src/json/` — the
   wire shapes Aster's codec already mirrors. Confirm by spot-check
   that the ports match.
2. `crates/model/src/modules/` — `MODULES_TABLE`, `ModuleMetadata`,
   `module_versions::ModuleSource`. Confirm the row shape your code
   parses. Note the `analyzeResult` field — Aster ignores it for
   now; understand *what* it is so you can decide when ignoring
   stops being safe.
3. `crates/model/src/source_packages/types.rs` — `SourcePackage`,
   `SerializedSourcePackage`. Confirm the JSON wire shape. Note the
   `external_packages_id` and `node_version` fields — Aster only
   cares about `isolate` environment, but your loader should refuse
   `node` modules with a typed error rather than silently misexecute.
4. `crates/isolate/src/environment/udf/mod.rs` — **the runner you are
   trying to reproduce a subset of**. Read it carefully. Pay
   attention to: how it wires `Convex.asyncSyscall`, how it sets up
   the module's import resolution, how it picks the right export to
   invoke for a given function path, how it serialises args + return.
5. `crates/isolate/src/environment/helpers/` — utility code the
   real isolate uses. Look at `module_loader.rs` (already linked in
   the integration doc) and `function_runner_helpers/` if present.
6. `crates/isolate/src/isolate/module_map.rs` (or equivalent) — how
   Convex represents the module graph in V8. You will need to either
   replicate this or take a much simpler approach.
7. `crates/storage_zip_reader/` — the upstream ZIP reader. **Important.**
   The `.blob` file is itself a ZIP. Convex's reader has its own
   constraints (streaming, lazy entry access). Read it; decide
   whether to reuse the same crate or use a stock `zip` crate.

After this, write a one-page memo titled "Module loading in Convex
upstream — relevant to Aster". Bullet form. Cover: what files Convex
expects in the ZIP, how it picks a module's main export, how
imports between modules are resolved (relative paths only? barrel
files? `_generated/api`?), how `convex/server` and `convex/values`
are made available (linked in or shimmed?), how `Convex.asyncSyscall`
is added to the global. The verifier will ask to see this memo.

### 4.3 Reproduce the existing real-VPS smoke

The integration doc has the operator runbook. Run through it:

1. SSH into `synapse-vps` (Hetzner CPX22 — credentials in `.vps/` of
   this repo, gitignored).
2. Pull latest `main` of both repos onto the VPS (`/tmp/convex-...`).
3. Run `setup.sh` to bring the stack up if it isn't already.
4. Use the existing dashboard / CLI to provision a `kind=aster`
   deployment. Confirm `docker ps` shows the brokerd container.
5. Hit `POST /v1/deployments/{name}/aster/invoke` with a trivial JS
   body (`globalThis.main = async () => 1;`) and confirm you get
   `stdout: '{"output":1,...}'` back.

If step 5 doesn't work — and it almost certainly won't, since the
v8cell image on the VPS likely predates `ASTER_JS_INLINE` — that's
the first concrete task: rebuild + push the image. Document exactly
what was missing in your investigation notes.

If step 5 *does* work without rebuilding, double-check by changing
the JS to something that reads from Postgres via
`Convex.asyncSyscall("1.0/get", ...)`. That covers the layer-7 path.

---

## 5. Concrete tasks, in priority order

### Task 5.1 — Rebuild and publish `aster-v8cell:0.4`

> **Why first.** Synapse's `aster/invoke` endpoint sets
> `ASTER_JS_INLINE`. The pre-PR-#16 image silently ignores it and
> will fail with "missing required env ASTER_JS". Without this
> rebuild, the rest of the pipeline cannot be smoke-tested.

**Status 2026-05-04:** done as a tarball-shipping slice, not a
registry-publish slice. Both Aster Docker smokes passed with tag `0.4`,
Synapse pins both broker and cell through `AsterImageTag`, and the VPS
raw-JS invoke returned `output:1`. The smoke also found and fixed an
image/default-env edge: `aster-v8cell` must not carry an image-level
`ASTER_JS` when Synapse invokes through `ASTER_JS_INLINE`; Synapse now
also clears `ASTER_JS=` defensively. Details live in
`docs/ASTER_VPS_SMOKE.md`.

**Goal.** Have a registry-pullable (or `docker save | scp`-able)
image whose v8cell binary supports `ASTER_JS_INLINE`. Bump the tag
in Synapse's `internal/docker/aster.go` and rebuild the brokerd
image too — they share a multi-stage Dockerfile and we don't want
the version mismatch between `aster-brokerd:0.3` and
`aster-v8cell:0.4`.

**Investigation prerequisites.**
- Read `aster-runner/docker/Dockerfile` end-to-end. Note the
  `runtime-broker` and `runtime-v8cell` targets.
- Read `aster-runner/docker/smoke.sh` — that's the smoke harness
  whose `tag` arg you are bumping.
- Decide: do we publish to a public registry, or `docker save`
  for now? Check whether `Iann29/aster` already has a release
  workflow. If yes, run it. If no, the tarball-shipping approach
  is fine for v0.5 but document it.

**Implementation strategy.**
- Bump tags in *both* `Dockerfile` references (broker + v8cell)
  and in `synapse/internal/docker/aster.go` (`AsterBrokerImage`,
  `AsterCellImage` constants).
- Rebuild + smoke locally first.
- Update `aster-runner/docker/smoke.sh`'s default tag.
- If publishing to a registry: add a step to the existing CI
  workflow that pushes on tag.
- Synapse-side: a separate one-line PR bumps the constants. Yours
  may bundle them; either is fine, just don't bundle unrelated
  changes.

**Tests.**
- `aster-runner/docker/smoke.sh 0.4` must pass.
- `aster-runner/docker/smoke-postgres.sh 0.4` (the 3-container
  variant) must pass.
- A new unit test: confirm the v8cell image embedded in
  Synapse's constant matches the latest tag. Avoid hard-coding
  twice.

**Acceptance criteria.**
- Both smokes green for the new tag.
- Synapse `go test ./...` green with the bumped constant.
- Real-VPS: `POST /aster/invoke` with `ASTER_JS_INLINE`-shaped
  payload returns `output:1` for the trivial `() => 1` JS.

**Pitfalls.**
- The Synapse compose stack hard-codes some image refs in
  `docker-compose.yml`. Find and update them.
- The dashboard's amber "aster" badge has nothing to do with the
  image version, but the operator's expectation might. Do not
  change UI copy in this PR.

### Task 5.2 — Real-VPS smoke of the existing pipeline

> **Why second.** Before adding more layers, prove the layers
> already there work end-to-end on a real VPS. CI tests are green;
> bash and Go tests are green; that does not mean the operator's
> happy path works on Hetzner.

**Goal.** A documented, reproducible run of: provision an Aster
deployment, invoke it with a `Convex.asyncSyscall("1.0/get")`-using
JS, get the right document back. Capture the exact commands +
output in a markdown file under `aster-runner/docs/` or in this
repo's `docs/`.

**Investigation prerequisites.**
- Task 5.1 must be done.
- The existing operator runbook in `docs/ASTER_INTEGRATION.md`.
- `aster-e2e-fixture/` — the Convex app fixture that PR #54
  added. Use it to seed a real `messages` table on a `kind=convex`
  deployment, *then* point a `kind=aster` deployment at the same
  Postgres so the `invoke` call can resolve real IDv6 strings.

**Implementation strategy.**
- Stand up two deployments: one `kind=convex` (where the fixture
  app gets deployed via `npx convex deploy`), one `kind=aster`
  (where the broker reads the same DB). The way Synapse models
  this depends on what currently works — investigate first.
  Worst case, you create the Aster deployment with `kind=aster`
  and manually point its env at the convex backend's Postgres.
- Run `seedIan` mutation on the convex-kind deployment. This
  writes a row.
- Find the resulting IDv6 from a manual psql against the database.
- Hand-craft a JS body that does
  `Convex.asyncSyscall("1.0/get", JSON.stringify({id: "<idv6>"}))`
  and call `aster/invoke` with it.
- Confirm the response stdout has the row's `name` field.

**Tests.**
- The output is a markdown file with the captured session.
  No CI test — this is one-shot validation.

**Acceptance criteria.**
- Document the result in `docs/ASTER_VPS_SMOKE.md` (new file).
  Include: the curl commands, the rows seeded in Postgres, the
  JS body, the cell stdout, the wall-clock timing.
- If anything fails, document the failure as a numbered issue.
  Do NOT fix issues you find here; *catalog* them, then file
  them as separate tasks. The point of this task is to map
  the broken edges, not paper over them.

**Pitfalls.**
- Synapse may not let you point two deployments at the same
  Postgres. If so, document that as an issue + propose a path
  forward (synthesise data manually via psql; or extend the
  Synapse model to accept "external Postgres" for kind=aster).
- The seal seed env on the brokerd vs. the secret used by
  Synapse's invoke endpoint must match. Verify before chasing
  ghosts when the cell errors out with capsule-decrypt failures.

### Task 5.3 — Brokerd: expose modules dir + new IPC verb

> **Why.** The cell needs to fetch bundle bytes for a module path
> from the broker. Today the broker's IPC surface has no such
> verb. Adding it is small and unblocks the cell-side loader.

**Goal.** A new `IpcRequest::LoadModuleBundle { context, capsule,
path }` variant + corresponding response, wired to
`PostgresCapsuleStore::load_module_bundle`. The brokerd binary
also reads a new `ASTER_MODULES_DIR` env var and threads it into
the `PostgresConfig`.

**Investigation prerequisites.**
- Read `crates/ipc/src/lib.rs`. Note the framing + how
  `IpcRequest`/`IpcResponse` are added (one match arm per variant
  end-to-end).
- Read `crates/broker/src/lib.rs` — there's a `CapsuleBrokerClient`
  trait. Decide whether the new verb belongs there (probably not —
  module loading is orthogonal to capsules; it has no
  context/capsule/snapshot_ts), or as a separate trait.
- Read `crates/ipc/src/bin/aster_brokerd.rs` for env-var parsing
  pattern. `ASTER_MODULES_DIR` is optional; missing means "module
  loading disabled" (same shape as the in-memory store).

**Implementation strategy.**
- Add the env var to `BrokerConfig`. Plumb it through to the
  Postgres backend's `PostgresConfig.modules_dir`.
- Decide on the IPC shape. Recommendation: not part of
  `CapsuleBrokerClient`; instead, a small separate trait
  (`ModuleSource`?) that brokerd's request loop calls when the
  IPC variant arrives. Keep capsule-related and module-related
  paths from cross-contaminating.
- Authentication-shape: the cell already proves it knows the
  seal seed (via the capsule it already holds). Whether the
  module-load IPC needs the same context / capsule handshake
  is a *design* question — does fetching a module require
  capability-scope, or is "you have a UDS connection" enough?
  Write a paragraph weighing this in the PR description before
  picking. Default recommendation: require `(context, capsule)`
  on the request, mirror `HydratePoint`'s pattern. Cells already
  have a capsule; passing it is cheap.
- Wire timeouts on the broker side. Module loads can be slow
  (FS reads, sha256 over a few hundred KB).

**Tests.**
- Unit: existing brokerd parsing tests gain `ASTER_MODULES_DIR`
  cases (set / unset / non-existent path).
- Integration: a new test in `crates/ipc/tests/process_boundary.rs`
  (or a new sibling) that spawns brokerd with the modules dir set
  and the in-memory store, hits the new IPC verb from a thin
  client, asserts the bytes round-trip.
- Postgres-it: a real-Postgres test in `aster-store-postgres`
  that seeds modules + writes the bundle to the FS dir, then
  drives the brokerd binary end-to-end.

**Acceptance criteria.**
- All workspace tests green. Postgres-it lane green.
- Docker smoke (`smoke.sh` + `smoke-postgres.sh`) still green.
- New CLI smoke: `smoke-postgres-modules.sh` (or extension of the
  existing one) shows the cell receiving bundle bytes via the
  new IPC verb and printing them on stdout.

**Pitfalls.**
- Don't add `tokio-postgres` or `deadpool-postgres` as
  dependencies of `aster-broker` or `aster-ipc`. The crate-
  boundary discipline (cells must NOT link Postgres deps) is
  load-bearing for the security model.
- The IPC framing is length-prefixed JSON. Bundle bytes can be
  ~100 KB; serializing as `Vec<u8>` in JSON means base64. That's
  fine for v0.5 but profile it before this becomes the hot path.
  Document the eventual binary-frame escape hatch in the PR.

### Task 5.4 — Cell-side module loader (the big one)

> **Why.** This is what gets the project from "operator can run
> handwritten JS that knows how to talk to the broker" to "a real
> Convex bundle deployed via `npx convex deploy` runs unchanged".
> It is the largest task on the list and likely two or three PRs.

**Goal.** The v8cell binary, given a module path (e.g.
`messages.js`) and a function name (e.g. `getById`), and an
arguments JSON string, fetches the bundle bytes from the broker,
unzips, picks the right entry, compiles it as a V8 ESM module
with the Convex shims, invokes the right export, and prints the
return value.

**Investigation prerequisites — non-negotiable.**
- The memo from §4.2 must exist before you start.
- Read upstream's `crates/isolate/src/isolate/module_map.rs` (or
  whatever the current name is) for how Convex sets up its
  module map in V8. Identify the *minimum* subset Aster needs.
- Read `crates/isolate/src/environment/udf/mod.rs` for the
  invocation shape. Note in particular how `udf` shells out to
  module exports — the export naming convention (`<funcName>`
  or `<funcName>.invokeQuery` or some wrapper).
- Read `convex/server/index.ts` and `convex/values/index.ts` in
  `npm-packages/convex/src/` — the JS-side helpers your shims
  need to expose.
- Read `npm-packages/convex/src/server/impl/module_imports.ts`
  if present — Convex's bundler injects helper modules at build
  time; understand what's already in the bundle vs. what you
  need to provide.
- Decide: do you need the full module graph, or is each module
  bundled standalone (so that `messages.js` already has its
  imports inlined)? Strongly suspect the latter — the Convex
  bundler emits per-module bundles. Confirm by running
  `npx convex deploy` against the e2e-fixture and dumping the
  `.blob`'s contents.

**Recommended subdivision (each a separate PR).**

#### 5.4.a Bundle ingestion

- Cell receives bundle bytes from the broker (via the IPC verb
  from Task 5.3).
- Unzips into an in-memory map: `{ "<entry path>": Vec<u8> }`.
- Picks the entry matching the requested module path.
- Returns the JS source string.
- Tests: a unit test against a hand-crafted ZIP. A smoke test
  that runs the brokerd with the e2e-fixture's bundle and
  confirms the cell can extract `messages.js`.

#### 5.4.b V8 ESM module instantiation

- Compile the JS as an ES module in V8 (`v8::ScriptOrigin`
  with `is_module: true`, `Module::compile`, `instantiate`).
- Module imports — first cut: reject anything other than
  `convex/server`, `convex/values`, and `./_generated/api`.
  Document the constraint clearly. Real bundles may already be
  flat (no relative imports left after the bundler runs); confirm
  in §4.2 memo.
- Convex shims: provide minimal stubs for `convex/server`'s
  `query()`, `mutation()`, `action()` factories (which the user's
  code calls to declare functions; the runner picks them off the
  module's exports later), and for `convex/values`' `v.string()`,
  etc., schema helpers. They don't need to do real validation
  for v0.5 — they just need to be present and chainable.
- Tests: compile a hand-crafted module that imports `convex/server`,
  exports a `query`, confirm V8 reports the export.

#### 5.4.c Function invocation

- Given a function name like `messages:getById`, look up the
  module's export of that name.
- The export is a `query`/`mutation` wrapped function. Inspect
  the shape upstream uses (likely `{ handler, args }` or similar).
  Call the handler with a constructed `ctx` object whose `db.get`
  hits `Convex.asyncSyscall("1.0/get", ...)`.
- Args codec: take the args JSON from the cell's input,
  `ConvexValue::from_json` it (PR #13 is for this), pass to the
  handler. Return value: `to_json` it back.
- Tests: end-to-end against the e2e-fixture's `messages:getById`
  query. The test seeds a row, runs the cell, expects the row
  back.

**Acceptance criteria.**
- A real `convex/_generated`-using module from the e2e-fixture
  bundle runs to completion when invoked through the cell.
- The cell's stdout matches what Convex's own runner produces
  for the same input (compare via the `kind=convex` deployment).
- All tests + lints green.

**Pitfalls.**
- V8 module compilation is fiddly. The `rusty_v8` API has
  undergone breaking changes between minor versions. Pin to the
  exact version the workspace uses and don't bump it for fun.
- Cells are sandboxed; they have no network, no filesystem (the
  bundle bytes come from the broker, not a mount). Anything you
  add to the shims that opens those capabilities is a P0 review
  blocker.
- Some Convex modules (`http.js`) declare HTTP routes; those
  aren't queries/mutations and don't follow the same invoke
  shape. Reject `http.js`-style modules with a typed error in
  this slice.
- `_generated/api` is a barrel that re-exports the user's
  modules under `api.messages.getById`-style paths. Decide:
  does your loader build a similar barrel at runtime, or do
  you require callers to address modules by path directly?
  Recommendation: path-direct for v0.5; barrel comes later.

### Task 5.5 — Synapse: mount modules dir into brokerd container

> **Why.** PR #17 made the modules dir a `PostgresCapsuleStore`
> config knob. PR #15 wired the index. Task 5.3 will add the
> brokerd env. None of that helps if Synapse's docker layer
> doesn't actually mount the host directory into the brokerd
> container.

**Goal.** A new field on `DeploymentSpec` (Aster-only), wired to
a host-path bind in `provisionAster`. Operator config decides
the host path; Synapse decides the in-container path
(recommend `/run/aster/modules`). The brokerd reads
`ASTER_MODULES_DIR=/run/aster/modules`.

**Investigation prerequisites.**
- Synapse `internal/config` package — find the env-var pattern.
- Synapse `internal/docker/aster.go` — `provisionAster`
  function. The bind block.
- Where do `kind=convex` deployments get their storage dir
  from today? Check `provisioner.go` and the storage-volume
  logic for the answer; mirror its pattern.

**Implementation strategy.**
- Synapse process config gains `AsterModulesDir` (host path).
  Wire from env or config file (whatever the existing pattern
  is — check `internal/config`).
- `DeploymentSpec` gains an optional `AsterModulesHostPath`
  field; the worker reads it from process config when
  `kind=aster`.
- `provisionAster` adds the bind + the env if the path is set.
  Keep working without it (operator hasn't configured it yet).
- Migration / config-doc update under `docs/`.

**Tests.**
- A new integration test: spec with `AsterModulesHostPath` set
  → assert the FakeDocker recorded the bind.
- An end-to-end test: a `kind=aster` deployment with the path
  set, then `aster/invoke` with JS that triggers a module load,
  expects success.

**Acceptance criteria.**
- Real-VPS: with the modules dir configured, `aster/invoke` JS
  that calls into a deployed module runs.
- Without the dir configured, brokerd starts cleanly and reads
  Postgres; only module loading fails with the typed error.

**Pitfalls.**
- The host path must be readable by the brokerd's UID. If the
  brokerd runs as a non-root user (it should), there's a
  permissions matrix to check.
- Convex's storage layer puts files under `<base>/modules/<key>.blob`.
  Aster's adapter takes the `<base>/modules` directory directly.
  The Synapse mount config should expose this distinction
  clearly to operators (a comment + a docs section).

### Task 5.6 — Convex-shaped HTTP frontend (`/api/query/<mod>:<fn>`)

> **Why.** Today the only way in is the raw-JS `aster/invoke`.
> A real Convex CLI / client expects `POST /api/query/...` with
> a function path + args.

**Goal.** A new Synapse route (probably mounted on the proxy
side, not the deployments side) that maps `/api/query/...` and
`/api/mutation/...` and `/api/action/...` to a cell invocation
on `kind=aster` deployments.

**Investigation prerequisites.**
- Read `synapse/internal/proxy/proxy.go` end-to-end. Note the
  current path-form vs. host-form routing for `kind=convex`
  deployments. The `/d/{name}/api/query/...` shape may need
  parsing.
- Look at how Convex CLI talks to a backend. The wire shape
  for `/api/query/<udfPath>` is:
  - body: `{ "path": "<module>:<function>", "args": {...} }`
  - response: `{ "status": "success" | "error", "value": ..., "logLines": [...] }`
- Confirm by running `npx convex` against a `kind=convex`
  deployment with a debug proxy.

**Implementation strategy.**
- Define a clear input/output shape that maps to your cell's
  IPC. The cell's existing JSON envelope might need a
  different output shape to satisfy the Convex client.
- For `kind=aster` deployments, the proxy intercepts
  `/api/query/...` (and the sister verbs) before the existing
  fall-through to a 502.
- Reuses `Docker.InvokeAsterCell` (built in PR #56) to spawn
  the cell, but with the cell side configured to enter the
  module loader path (Task 5.4) instead of the `ASTER_JS_INLINE`
  raw-JS path.
- Logging: the Convex client expects `logLines` in the response.
  Cell stderr / `console.log` interception is in scope; keep it
  simple (just stderr lines).

**Tests.**
- Synapse integration: a kind=aster deployment, a fake docker
  layer that returns a canned envelope, the proxy returns the
  right shape.
- E2E: real cell, real Postgres, real bundle. The output of
  this test is the same row as Task 5.2's manual VPS smoke,
  but driven through `/api/query/messages:getById` instead.

**Acceptance criteria.**
- `npx convex` (or a hand-crafted equivalent) against an Aster
  deployment runs queries successfully.
- Errors produce the Convex-shaped error envelope, not a Synapse
  one.

**Pitfalls.**
- Mutations + actions write data; this slice is read-only. Reject
  mutations/actions with a typed error if you can't support them
  in this PR.
- The Convex client's reconnect / streaming logic for `subscribe`
  is *not* in scope. HTTP polling is fine for v0.5.
- `kind=convex` deployments must continue working unchanged. Do
  not regress the existing proxy.

### Task 5.7 — Documentation update

> **Why.** When all of the above lands, `docs/ASTER_INTEGRATION.md`
> is out of date. Update it.

**Goal.** A clean, single-source-of-truth integration doc that
reflects the new state. Bump the README of each repo.

**Acceptance criteria.**
- The "TL;DR" of the integration doc says, truthfully, that
  real Convex apps run end-to-end.
- "What still doesn't work" is *empty* for the v0.5 promise, or
  lists the remaining gaps with concrete next steps.
- All file path references in docs point to existing files.

---

## 6. Common pitfalls (read once before starting any task)

These have bitten the project before. Don't repeat:

1. **bash `set -e` swallow.** Pattern `[[ $cond ]] && cmd` returns
   non-zero from a function when `$cond` is false; under `set -e`
   that aborts the function. Use explicit `if`. Bats tests don't
   inherit `set -e`, so they miss this.

2. **Image pulls vs. builds.** `docker compose pull` skips services
   declared as `build:`. Always `docker compose build && docker compose up -d`
   when iterating on images.

3. **camelCase vs. snake_case JSON.** OpenAPI shapes are camelCase.
   Postgres columns are snake_case. Bridge points need explicit
   tags. Decode with `DisallowUnknownFields` so a typo fails
   loudly instead of silently dropping a field.

4. **`NEXT_PUBLIC_*` env at build vs. runtime.** Next.js inlines
   these at build time. Mocked dashboard tests never notice; real
   browsers do.

5. **Convex Postgres lease.** A single-writer-per-deployment lease
   sits in the `leases` table. Aster only reads, so you don't trip
   it. But writing to `documents` from outside the Convex backend
   *will* break the lease invariant. Don't do that — even in tests.

6. **Capsule snapshot consistency.** The cell's `Convex.asyncSyscall`
   returns data at the cell's snapshot_ts, not "now". For read
   queries this is the correct semantics; for actions it isn't.
   When/if you implement actions, this is where the design
   conversation lives.

7. **IDv6 vs. Aster wire form.** `aster-store-postgres` accepts both.
   The cell's `Convex.asyncSyscall` should pass through whatever
   the JS handed it (typically IDv6). Don't transform on the cell
   side — let the broker dispatch.

8. **Docker-out-of-docker host paths.** Synapse runs in a
   container; spawned containers' bind mounts are interpreted by
   the *host* daemon, not the synapse container. So when synapse
   binds `/host/path:/in/container`, the `/host/path` must exist
   on the host, not inside synapse. Same gotcha for the modules
   dir mount in Task 5.5.

9. **CI clippy is not run with `-D warnings`** today, only the
   build is. If you add `cargo clippy` to CI, expect a flurry of
   pre-existing nits to need cleanup. Either fix them all in a
   prep PR or don't add it.

10. **Real-VPS validation is not optional** for changes that touch
    `setup.sh`, `installer/`, `docker-compose.yml`, or any backend
    handler that emits a URL. Bats and Go tests are insufficient.
    The PR #19 history catalogues 6 bugs that all had green CI;
    each is now in regression tests. Don't be the seventh.

---

## 7. How to coordinate with the verifier

Before starting any task:
- Skim this entire document, plus `docs/ASTER_INTEGRATION.md`.
- Run all CI lanes locally on `main` of both repos. Confirm
  green.
- Open a draft PR or scratch branch with your investigation
  notes (the §4 memos). Tag the verifier.

When stuck:
- Articulate the specific question. "How does X work?" is OK if
  you've spent time first; "what should I do?" is not.
- Quote the file:line you read that left you with the question.
- Propose two options and your tilt before asking.

When done with a task:
- Run all CI lanes locally one more time.
- Push the branch.
- Open a PR with the agreed structure (Summary / What's in /
  Tests / Test plan).
- Mention the task number (5.x) and the acceptance criteria
  you met.
- Tag the verifier for review.

If a task spans multiple PRs (5.4 will), open them sequentially.
Each PR should leave `main` in a buildable, releasable state — no
half-implemented features even temporarily. If the next slice
requires a stub, the stub must error cleanly, not panic.

---

## 8. Success looks like

- A real Convex application — `aster-e2e-fixture` is the easiest
  candidate — deployed against a Convex backend, registered as a
  `kind=aster` deployment in Synapse, can be invoked via
  `npx convex` (or a curl equivalent) and returns the right answer.
- All workspace tests + integration lanes green on both repos'
  `main`.
- Docs updated; nothing in `docs/ASTER_INTEGRATION.md` is
  contradicted by the code.
- A real-VPS smoke document, captured during validation, lives
  in `docs/`.
- The verifier signs off PR by PR.

When you reach this state, move on to the deferred items
(`#97 by_id index`, `#103+ production hardening`, `#109+
research items`). Those are documented in the project's task
tracker; you do not need them for the v0.5 promise.

Good luck. Read first, write second.
