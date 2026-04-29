---
name: synapse-test
description: Exercise the Synapse API end-to-end with curl flows that cover the happy path plus the standard 401/403/404/409 negative cases. Use when the user asks to "test the API", "verify the backend", "run smoke tests", or after touching auth/teams/projects/deployments handlers.
---

# Synapse smoke-test patterns

Each handler's commit message lists the canonical curl flow. The pattern is:

1. Truncate DB (see synapse-dev skill).
2. Register → grab access token.
3. Run the new flow + at least one negative case.
4. Confirm DB state with `psql` queries when the API doesn't expose it.

## Example — full flow through teams + projects

```bash
# Reset
PGPASSWORD=synapse psql -h localhost -U synapse -d synapse -c \
  "TRUNCATE users, teams, projects, team_members, deployments, project_env_vars, \
   team_invites, deploy_keys, access_tokens, audit_events RESTART IDENTITY;"

# Register
REG=$(curl -sf -X POST http://localhost:8080/v1/auth/register \
       -H 'Content-Type: application/json' \
       -d '{"email":"a@b.c","password":"strongpass123","name":"A"}')
A=$(echo "$REG" | python3 -c "import sys,json; print(json.load(sys.stdin)['accessToken'])")

# Team + project
curl -sf -X POST http://localhost:8080/v1/teams/create_team \
  -H "Authorization: Bearer $A" -H 'Content-Type: application/json' \
  -d '{"name":"My Team"}'
P=$(curl -sf -X POST http://localhost:8080/v1/teams/my-team/create_project \
     -H "Authorization: Bearer $A" -H 'Content-Type: application/json' \
     -d '{"projectName":"My Project"}')
PID=$(echo "$P" | python3 -c "import sys,json; print(json.load(sys.stdin)['projectId'])")

# Env vars
curl -sf -X POST "http://localhost:8080/v1/projects/$PID/update_default_environment_variables" \
  -H "Authorization: Bearer $A" -H 'Content-Type: application/json' \
  -d '{"changes":[{"op":"set","name":"FOO","value":"bar"}]}'

# Negative — anonymous request, expect 401
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/v1/me  # → 401

# Negative — outsider hitting team, expect 403
B=$(curl -sf -X POST http://localhost:8080/v1/auth/register \
     -H 'Content-Type: application/json' \
     -d '{"email":"x@y.z","password":"strongpass123","name":"B"}' \
   | python3 -c "import sys,json; print(json.load(sys.stdin)['accessToken'])")
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/v1/teams/my-team \
  -H "Authorization: Bearer $B"  # → 403
```

## When to write a Go test instead

If the same logic needs verifying across many inputs (e.g. the slug allocator),
write a Go unit test. curl flows are for *integration* coverage; pure-function
correctness belongs in `_test.go`.

## What this skill does NOT cover

- Provisioner tests — those need a Docker daemon and a Convex backend image.
- Dashboard tests — that fork lives under `dashboard/` and uses a Next.js
  test stack.
