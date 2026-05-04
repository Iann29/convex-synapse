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

## Pending: fixture-backed `Convex.asyncSyscall("1.0/get")`

Task 5.2 is still open. The next smoke should deploy `aster-e2e-fixture`,
seed a real row through the Convex path, then invoke Aster JS that calls
`Convex.asyncSyscall("1.0/get", ...)` against that row and records the returned
document.
