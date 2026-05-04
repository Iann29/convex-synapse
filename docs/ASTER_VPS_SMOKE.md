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
