# Aster E2E Convex fixture

The minimal Convex app used to validate Aster's read path end-to-end.
One table, two fields, one indexed lookup, one mutation, one query.

## Why this shape

- `messages` table with `{ name, body }` — small, deterministic, easy to
  read back from raw Postgres bytes.
- `by_name` index — gives us a future second-stage test against
  `1.0/queryStreamNext` (commented out in `convex/messages.ts`).
- `seedIan` mutation — produces a known-shape row so the Aster broker
  has predictable bytes to read.
- `getById` query — fires exactly one `1.0/get` async syscall, the
  smallest viable path the Aster v8cell has to serve.

## Zero-to-Postgres-row recipe

```bash
# 1. Install deps in the fixture
cd aster-e2e-fixture && npm install

# 2. Mint Synapse-side resources (one-time, against the VPS)
TOKEN=$(curl -s -X POST http://178.105.62.81:8080/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" | jq -r .accessToken)

PROJECT_ID=$(curl -s -H "Authorization: Bearer $TOKEN" \
  http://178.105.62.81:8080/v1/teams/<teamId>/list_projects | jq -r '.[0].id')

DEP_NAME=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"type":"dev"}' \
  http://178.105.62.81:8080/v1/projects/$PROJECT_ID/create_deployment | jq -r .name)

curl -s -H "Authorization: Bearer $TOKEN" \
  http://178.105.62.81:8080/v1/deployments/$DEP_NAME/cli_credentials > creds.json

export CONVEX_SELF_HOSTED_URL=$(jq -r .convexUrl creds.json)
export CONVEX_SELF_HOSTED_ADMIN_KEY=$(jq -r .adminKey creds.json)

# 3. Push the fixture functions
npx convex deploy

# 4. Seed
npx convex run messages:seedIan '{}'    # returns the new _id
ID=<paste returned id>

# 5. Read it back through Convex (no Aster yet)
curl -s "$CONVEX_SELF_HOSTED_URL/api/query" \
  -H 'Content-Type: application/json' \
  -d "{\"path\":\"messages:getById\",\"args\":{\"id\":\"$ID\"},\"format\":\"json\"}"

# Expected:
#   {"status":"success","value":{"_id":"...","_creationTime":...,"name":"ian","body":"hello"},"logLines":[]}
```

## Where Aster comes in

Once the v8cell speaks `Convex.asyncSyscall("1.0/get", ...)` instead of
the toy `Aster.read("key", "field")`, the same `getById` query can be
served by Aster's v8cell talking to the Postgres-backed broker — same
backend, same row, different execution plane.

The Postgres reads are already wired (see Aster's
`crates/store-postgres/`). The remaining pieces are the JS-runtime
syscall handler and IDv6 ↔ `<table_hex>/<id_hex>` translation. See
`Convex JS runtime research` memo handed off alongside this fixture.
