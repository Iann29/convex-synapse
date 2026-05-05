# Aster VPS smoke log

This file captures real-VPS validation for the Aster execution path.
Runbook commands intentionally use placeholders for credentials and hostnames;
the actual VPS credentials stay in `.vps/` and are not committed.

## 2026-05-04 — image `0.4` raw-JS invoke

Scope: Task 5.1 from `docs/AGENT_HANDOFF_ASTER_NEXT.md`. This proves the
current Synapse raw-JS endpoint can create a `kind=aster` deployment, start a
real broker container, spawn a real v8cell over the shared UDS volume, return
cell stdout, and delete the deployment again.

### Image transport

The Aster repo does not publish registry images yet. Images were built locally
and shipped to `synapse-vps` as a tarball:

```bash
cd /home/ian/aster-runner
docker build -f docker/Dockerfile --target runtime-broker -t aster-brokerd:0.4 .
docker build -f docker/Dockerfile --target runtime-v8cell -t aster-v8cell:0.4 .
docker/smoke.sh 0.4
docker/smoke-postgres.sh 0.4
docker save -o /tmp/aster-images-0.4.tar aster-brokerd:0.4 aster-v8cell:0.4
scp /tmp/aster-images-0.4.tar synapse-vps:/tmp/aster-images-0.4.tar
ssh synapse-vps 'docker load -i /tmp/aster-images-0.4.tar'
```

VPS image IDs after load:

```text
aster-v8cell:0.4  b53ab768191f
aster-brokerd:0.4 baf5eba1cd42
```

### Bug found during smoke

The first VPS invoke failed with:

```text
aster_v8cell: set ASTER_JS or ASTER_JS_INLINE, not both
```

Root cause: the v8cell image had `ENV ASTER_JS=/tenant/main.js`; Synapse passed
`ASTER_JS_INLINE`, so the binary saw two mutually exclusive sources. The fix was
two-sided:

- Aster Dockerfile no longer sets image-level `ASTER_JS`.
- Synapse clears `ASTER_JS=` before setting `ASTER_JS_INLINE`.

### VPS API flow

After loading corrected images and rebuilding the Synapse service on the VPS:

```bash
API=http://127.0.0.1:8080
TOKEN=$(curl -fsS -X POST "$API/v1/auth/register" \
  -H 'Content-Type: application/json' \
  -d '{"email":"aster-smoke-<timestamp>@example.test","password":"supersecret123","name":"Aster Smoke"}' \
  | jq -r .accessToken)

TEAM_SLUG=$(curl -fsS -X POST "$API/v1/teams/create_team" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Aster Smoke"}' | jq -r .slug)

PROJECT_ID=$(curl -fsS -X POST "$API/v1/teams/$TEAM_SLUG/create_project" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"projectName":"Aster Smoke"}' | jq -r .project.id)

DEPLOYMENT=$(curl -fsS -X POST "$API/v1/projects/$PROJECT_ID/create_deployment" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"type":"dev","kind":"aster"}' | jq -r .name)

curl -fsS -X POST "$API/v1/deployments/$DEPLOYMENT/aster/invoke" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"js":"globalThis.main = async () => 1;"}'
```

Captured result:

```text
install_status={"firstRun":false,"version":"1.1.1"}
deployment=<generated>
broker_image=aster-brokerd:0.4
invoke_exit_code=0
invoke_output=1
invoke_traps=0
```

Cleanup:

```bash
curl -fsS -X POST "$API/v1/deployments/$DEPLOYMENT/delete" \
  -H "Authorization: Bearer $TOKEN"
docker ps -a --filter label=synapse.kind=aster
```

The failed first-run deployment and the successful smoke deployment were both
deleted through the Synapse API; no managed Aster containers remained afterward.

## 2026-05-04 — fixture-backed `Convex.asyncSyscall("1.0/get")` attempt

Scope: Task 5.2 from `docs/AGENT_HANDOFF_ASTER_NEXT.md`. This run proves the
fixture deploys and reads correctly through a normal Synapse-managed Convex
deployment, then maps the currently broken edge between that deployment's
storage and the Aster broker.

### Synapse/VPS storage preflight

The VPS install used for this run was still the non-HA test stack:

```text
SYNAPSE_PUBLIC_URL=http://178.105.62.81:8080
SYNAPSE_HA_ENABLED=false
SYNAPSE_BACKEND_POSTGRES_URL=<redacted>
SYNAPSE_BACKEND_S3_ENDPOINT=
```

That matters because ordinary Convex deployments use SQLite in a Docker volume
when `ha:false`. Aster's Postgres-backed read path cannot see those rows.

### Fixture deploy and seed

The fixture was copied to a temporary directory so `npx convex deploy` could
generate `_generated/` without modifying the repository:

```bash
workdir=$(mktemp -d /tmp/aster-e2e-fixture-smoke.XXXXXX)
cp -a /home/ian/convex-2/aster-e2e-fixture/. "$workdir/"
cd "$workdir"

CONVEX_SELF_HOSTED_URL=http://178.105.62.81:3210 \
CONVEX_SELF_HOSTED_ADMIN_KEY=<redacted> \
  npx convex deploy --typecheck disable \
  --message "Aster Task 5.2 VPS fixture smoke"

CONVEX_SELF_HOSTED_URL=http://178.105.62.81:3210 \
CONVEX_SELF_HOSTED_ADMIN_KEY=<redacted> \
  npx convex run messages:seedIan "{}"
```

Deploy output:

```text
✔ Added table indexes:
  [+] messages.by_name   name, _creationTime
✔ Deployed Convex functions to http://178.105.62.81:3210
```

Seeded row:

```text
id=j570c3a2vfz8js99c3t12b68hh863egz
```

Control read through the Convex backend:

```bash
curl -fsS "$CONVEX_SELF_HOSTED_URL/api/query" \
  -H 'Content-Type: application/json' \
  -d '{"path":"messages:getById","args":{"id":"j570c3a2vfz8js99c3t12b68hh863egz"},"format":"json"}'
```

Captured result:

```json
{"status":"success","value":{"_creationTime":1777907009446.7734,"_id":"j570c3a2vfz8js99c3t12b68hh863egz","body":"hello","name":"ian"}}
```

Wall clock for the repeated control query: `396ms`.

### Aster invocation against the same ID

An Aster deployment was created in the same project and reached `running` with
broker image `aster-brokerd:0.4`.

JS body:

```js
globalThis.main = async () => {
  const json = await Convex.asyncSyscall(
    "1.0/get",
    JSON.stringify({ id: "j570c3a2vfz8js99c3t12b68hh863egz" })
  );
  return JSON.parse(json);
};
```

API call:

```bash
curl -fsS -X POST "$API/v1/deployments/$ASTER_DEPLOYMENT/aster/invoke" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @/tmp/aster-task52.aster_payload.json
```

Captured result:

```json
{"stdout":"{\"capsule_hash\":16672643915685477528,\"output\":null,\"traps\":1}\n","exitCode":0}
```

Wall clock for the repeated Aster invoke: `681ms`.

### Evidence for the broken edge

Deployment rows:

```text
merry-ferret-2979|aster|running|false|NULL
sturdy-newt-6188|convex|running|false|3210
deployment_storage_count=0
```

Container storage shape:

```text
convex_container=convex-sturdy-newt-6188
convex_env_storage:
convex_mounts:
synapse-data-sturdy-newt-6188 /convex/data

aster_container=aster-broker-merry-ferret-2979
aster_env_storage:
ASTER_SEED_I64=
ASTER_SNAPSHOT_TS=0
aster_mounts:
synapse-aster-merry-ferret-2979 /run/aster
```

Interpretation:

1. The Convex fixture row was not seeded in Postgres. It lives in the
   single-replica SQLite volume because the VPS has `SYNAPSE_HA_ENABLED=false`
   and the deployment was created with `ha:false`.
2. The Aster broker was not pointed at Postgres. Synapse's current
   `provisionAster` path starts brokerd without `ASTER_STORE=postgres` or
   `ASTER_DB_URL`; it boots the memory-store path with empty seeds.
3. The Aster cell did execute and trap once (`traps:1`), but the broker had no
   backing store containing the IDv6 row, so the read returned `null`.

Task 5.2 remains open as a fixture-backed success criterion. The next attempt
needs a real shared Postgres source of truth: either enable HA storage on the
VPS and create the Convex fixture deployment with `ha:true`, or add the planned
Synapse wiring that lets `kind=aster` point at an existing Convex deployment's
Postgres/modules storage.

## 2026-05-04 — PR #59 Aster bridge config upgrade smoke

Scope: Synapse PR #59. This validates the real installer/compose path for the
new optional Aster runtime bridge env vars, without resetting the VPS.

Upgrade command:

```bash
cd /opt/synapse-test
bash setup.sh --upgrade \
  --ref=feat/aster-modules-dir-wiring \
  --force \
  --install-dir=/opt/synapse-test \
  --non-interactive
```

Captured outcome:

```text
Upgrade complete: 1.1.1 → feat-aster-modules-dir-wiring
health={"status":"ok","version":"1.1.1","database":"ok","proxyEnabled":true}
```

The running `synapse-api` container had the new compose pass-through envs:

```text
SYNAPSE_ASTER_DB_SCHEMA=public
SYNAPSE_ASTER_MODULES_DIR=
SYNAPSE_ASTER_POSTGRES_URL=
```

The empty values are intentional for this smoke: they prove the new vars are
present while preserving the existing memory-store raw-JS path. A follow-up
fixture smoke should fill them with a real shared Convex Postgres database and
modules host path after the cell-side module loader lands.

Regression flow:

```bash
API=http://127.0.0.1:8080
# register user, create team/project, create {"type":"dev","kind":"aster"}
curl -fsS -X POST "$API/v1/deployments/$DEPLOYMENT/aster/invoke" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"js":"globalThis.main = async () => 59;"}'
```

Captured result:

```text
exitCode=0 output=59 traps=0
```

Cleanup:

```bash
curl -fsS -X POST "$API/v1/deployments/$DEPLOYMENT/delete" \
  -H "Authorization: Bearer $TOKEN"
docker ps -a --filter label=synapse.kind=aster
```

No managed Aster containers remained after cleanup.

## 2026-05-05 — Synapse-driven module-mode invoke

Scope: Slice A from `docs/AGENT_HANDOFF_ASTER_NEXT.md`. This is the proof
that an end-to-end real Convex bundle (`messages.bundled.js`, the 58 KB
output of `npx convex deploy`) executes inside a v8cell driven by
Synapse's own `POST /v1/deployments/{name}/aster/invoke` endpoint —
NOT a raw `docker run`. The cell loads the bundle from brokerd's
modules-storage, calls the named export `getById` as a Convex query,
the JS handler fires `Convex.asyncSyscall("1.0/get", {id})` which
traverses brokerd → Postgres → seeded document, and the resolved
JSON shape lands back in the API response.

The PR landed in [#60](https://github.com/Iann29/convex-synapse/pull/60)
on 2026-05-05; commit `53e4ebd`. Slice 1 of the brief.

### Pre-flight

VPS was reset to a fresh Ubuntu 24.04 image with no Docker installed.
Image transport:

```bash
# (dev box) build from aster-runner main
cd /home/ian/aster-runner
docker build --target=runtime-broker -t aster-brokerd:0.4 -f docker/Dockerfile .
docker build --target=runtime-v8cell -t aster-v8cell:0.4 -f docker/Dockerfile .
docker/smoke.sh 0.4
docker/smoke-postgres.sh 0.4
docker/smoke-bundle.sh 0.4   # local module-query dress rehearsal
docker save -o /tmp/aster-images-0.4.tar aster-brokerd:0.4 aster-v8cell:0.4
scp /tmp/aster-images-0.4.tar synapse-vps:/tmp/aster-images-0.4.tar
scp /home/ian/aster-runner/crates/v8cell/tests/fixtures/messages.bundled.js synapse-vps:/tmp/messages.bundled.js
scp /home/ian/aster-runner/crates/store-postgres/tests/fixtures/schema.sql synapse-vps:/tmp/aster-schema.sql
```

The 0.4 tag was rebuilt against the current `Iann29/aster` main —
older 0.4 images on disk predated PRs #20-23 (cell-side bundle loader
+ `ASTER_FUNCTION_NAME`/`ASTER_ARGS_JSON` env wiring) and would
reject module-mode requests with
`missing required env ASTER_JS or ASTER_JS_INLINE`. The local
`docker/smoke-bundle.sh 0.4` run is the dress rehearsal that catches
this before the VPS handoff.

### Synapse install

```bash
ssh synapse-vps 'curl -fsSL https://get.docker.com | sh'   # VPS had no docker
ssh synapse-vps 'cd /tmp && rm -rf convex-synapse && git clone https://github.com/Iann29/convex-synapse.git'
ssh synapse-vps 'cd /tmp/convex-synapse && bash setup.sh \
    --no-tls --skip-dns-check --non-interactive --install-dir=/opt/synapse-test'
```

After ~3 min the installer's self-test landed at
`{"firstRun":true,"version":"1.1.1"}` against
`http://178.105.62.81:8080/v1/install_status`. The slice-1 commit
(`53e4ebd`) was on main at clone time so the built `synapse` binary
contained the `mutually_exclusive_modes` / `incomplete_module_mode`
codes (verified by grepping the Go source on disk).

### Aster image load

```bash
ssh synapse-vps 'docker load -i /tmp/aster-images-0.4.tar'
# Loaded image: aster-brokerd:0.4
# Loaded image: aster-v8cell:0.4
```

VPS image IDs after load:

```text
aster-brokerd:0.4  b36c522bfb88
aster-v8cell:0.4   3935fdbf41ee
```

### Schema + seed strategy

To keep the install minimal we reused Synapse's own `synapse-postgres`
container as the broker's data source. A `convex_dev` schema lives
alongside the regular Synapse tables in the same database; the
brokerd connects via the synapse-network's internal `postgres:5432`
hostname. No second postgres container, no extra volume.

The seeding script (`/tmp/seed-aster.sh` on the VPS) mirrors what
`aster-runner/docker/smoke-bundle.sh` does against a sibling
container, retargeted at Synapse's postgres:

1. Stages the bundle ZIP at `/opt/aster-modules/test-bundle.blob`
   (host path the brokerd binds read-only at `/run/aster/modules`).
   Layout: `modules/messages.js` + `modules/messages.js.map` +
   `metadata.json`. SHA-256 captured for `_source_packages.sha256`.
2. Drops + creates `convex_dev` schema (`schema.sql` from the aster
   fixtures dir).
3. Inserts:
   - `persistence_globals` rows (`max_repeatable_ts=200`,
     `tables_table_id=zMzMzMzMzMzMzMzMzMzMzA`).
   - `_tables` rows for `messages` (#10001), `_modules` (#8002),
     `_source_packages` (#8001) — all `state=active`.
   - User document `0123.../aaaa...` with body
     `{"name":"ian","_id":"messages|aaaa..."}` at ts=100.
   - `_source_packages` body row (storageKey + sha256-of-blob).
   - `_modules` body row whose `sourcePackageId` is the IDv6 string
     `r4zexvjnaqqewnanxvq5anfexsana5t4` (computed via
     `aster-convex-codec/examples/idv6_smoke_helper -- 8001
     eeee5555...`).

Validation row count (`SELECT count(*), table_id FROM
convex_dev.documents GROUP BY table_id`):

```text
2 rows under 0123...     (user docs)
1 row  under aaaa1111... (_modules body)
1 row  under bbbb2222... (_source_packages body)
3 rows under cccccccc... (_tables: messages + _modules + _source_packages)
```

### Wiring the SYNAPSE_ASTER_* envs

```bash
ssh synapse-vps
cd /opt/synapse-test
# .env edits (passwords redacted):
#   SYNAPSE_ASTER_POSTGRES_URL=postgres://synapse:<PG_PASSWORD>@postgres:5432/synapse?sslmode=disable
#   SYNAPSE_ASTER_DB_SCHEMA=convex_dev
#   SYNAPSE_ASTER_MODULES_DIR=/opt/aster-modules
docker compose up -d   # `restart` does NOT re-read .env — `up` does
docker inspect synapse-api --format '{{range .Config.Env}}{{.}}{{println}}{{end}}' | grep ASTER
```

Confirms the three envs landed in the synapse-api container.

### The invoke

```bash
API=http://178.105.62.81:8080
EMAIL=aster-modulequery-$(date +%s)@example.test

TOKEN=$(curl -fsS -X POST "$API/v1/auth/register" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$EMAIL\",\"password\":\"supersecret123\",\"name\":\"Aster Module Smoke\"}" \
    | jq -r .accessToken)
TEAM_SLUG=$(curl -fsS -X POST "$API/v1/teams/create_team" -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' -d '{"name":"Aster Module Smoke"}' | jq -r .slug)
PROJECT_ID=$(curl -fsS -X POST "$API/v1/teams/$TEAM_SLUG/create_project" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{"projectName":"Aster Module Smoke"}' | jq -r .project.id)
DEPLOYMENT=$(curl -fsS -X POST "$API/v1/projects/$PROJECT_ID/create_deployment" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{"type":"dev","kind":"aster"}' | jq -r .name)

# Wait for status=running, then verify brokerd is up:
ssh synapse-vps "docker ps --filter name=aster-broker --format '{{.Names}} {{.Status}}'"
# aster-broker-patient-heron-3280 Up 2 seconds

WIRE_ID="0123456789abcdef0123456789abcdef/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
INVOKE_BODY=$(jq -n \
    --arg modulePath "messages.js" \
    --arg functionName "getById" \
    --arg argsJson "[{\"id\":\"${WIRE_ID}\"}]" \
    '{modulePath:$modulePath, functionName:$functionName, argsJson:$argsJson, snapshotTs:200}')

curl -fsS -X POST "$API/v1/deployments/$DEPLOYMENT/aster/invoke" \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    --data-binary "$INVOKE_BODY"
```

`snapshotTs:200` is required because the seed sets
`max_repeatable_ts=200`; the broker enforces snapshot equality and
rejects ts=0 (the zero default) with
`snapshot_ts 0 is not broker snapshot 200`. Future versions of the
broker may relax this; for now the operator must pin the ts.

Captured response (deployment `patient-heron-3280`):

```json
{
  "stdout": "{\"capsule_hash\":14870856087804244365,\"output\":\"{\\\"_id\\\":\\\"messages|aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\\\",\\\"name\\\":\\\"ian\\\"}\",\"traps\":1}\n",
  "exitCode": 0
}
```

The `output` field is a JSON-encoded string holding the document
the bundle's `getById` query resolved — `name=ian` came back
through `Convex.asyncSyscall("1.0/get")` → broker → postgres →
sealed capsule → cell. `traps:1` matches the single `db.get(id)`
the bundle issues. `exitCode:0` confirms the cell ran to completion.

### Cleanup

```bash
curl -fsS -X POST "$API/v1/deployments/$DEPLOYMENT/delete" \
    -H "Authorization: Bearer $TOKEN"
# {"name":"patient-heron-3280","status":"deleted"}

ssh synapse-vps "docker ps -a --filter label=synapse.kind=aster --format '{{.Names}} {{.Status}}'"
ssh synapse-vps "docker volume ls --filter name=synapse-aster --format '{{.Name}}'"
```

Both queries return empty: brokerd container + UDS volume removed,
no orphans. The seeded `convex_dev` schema in synapse-postgres
intentionally stays so the smoke is repeatable; drop with
`DROP SCHEMA convex_dev CASCADE` if desired.

### Pitfalls hit during this run

- **Stale 0.4 image.** The dev box already had `aster-brokerd:0.4` /
  `aster-v8cell:0.4` from before the cell-side module-loader merged.
  First invoke returned
  `aster_v8cell: missing required env ASTER_JS or ASTER_JS_INLINE`.
  Fix: rebuild both images from current main BEFORE shipping the
  tarball. The local `docker/smoke-bundle.sh 0.4` rehearsal is
  what catches this — keep it in the runbook.
- **`docker compose restart` doesn't re-read `.env`.** After editing
  `SYNAPSE_ASTER_*`, `restart` started synapse-api with the OLD env.
  `docker compose up -d` (which recreates the container with the new
  env) is the right command.
- **Snapshot ts pinning.** `snapshotTs:0` (default) → broker rejects
  with "snapshot_ts 0 is not broker snapshot 200". The seed pins
  `max_repeatable_ts=200`; the cell must request the same ts. This
  is a property of the broker's Postgres snapshot pinning, not a
  Synapse bug.
