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

The v8cell already speaks `Convex.asyncSyscall("1.0/get", ...)`, and
Aster's Postgres store already accepts both `<table_hex>/<id_hex>` and
Convex IDv6 strings. This fixture is the shared-row smoke target: deploy
it through a normal Convex backend, seed one message, then invoke a
`kind=aster` cell against the same Postgres storage and ask for that ID.

The current gap is not the syscall or ID codec. The next successful smoke
needs shared Postgres/modules storage wired into brokerd, then the larger
cell-side module loader + Convex-shaped HTTP frontend so the fixture can run
as `messages:getById` instead of hand-written raw JS.
