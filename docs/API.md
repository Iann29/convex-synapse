# API reference

Synapse implements (a subset of) Convex Cloud's stable
[Management API v1](https://github.com/get-convex/convex-backend/blob/main/npm-packages/dashboard/dashboard-management-openapi.json).
Endpoints below are grouped by resource. Compatibility with the OpenAPI spec
is noted as ✅ (matches), 🔧 (custom — Synapse extension), or 📍 (Cloud-style
endpoint with a smaller payload).

All authenticated endpoints expect `Authorization: Bearer <token>` where the
token is either:
- A JWT issued by `/v1/auth/login` (15-minute lifetime by default), or
- A `syn_*` opaque personal-access token (created via the dashboard's
  `/me` page or via `POST /v1/create_personal_access_token` — see below).

## Health

### `GET /health`

Returns `{status, version, database}`. Status is `ok` or `degraded`.

## Auth (custom — Cloud has WorkOS OAuth flows we don't replicate)

### `POST /v1/auth/register` 🔧

Body: `{email, password, name?}`. 8-char min password.
Returns: `{accessToken, refreshToken, tokenType:"Bearer", expiresIn, user}`.

### `POST /v1/auth/login` 🔧

Body: `{email, password}`. Same response shape as register.

### `POST /v1/auth/refresh` 🔧

Body: `{refreshToken}`. Returns a new token pair.

## Profile

### `GET /v1/me` ✅ (alias `/v1/profile`)

Returns the authenticated user.

## Teams

### `POST /v1/teams/create_team` ✅

Body: `{name, defaultRegion?}`. Returns the new `Team`. Slug auto-generated.

### `GET /v1/teams` 🔧

Lists teams the caller belongs to.

### `GET /v1/teams/{ref}` ✅

`ref` is either the UUID or the slug. Returns `Team`.

### `GET /v1/teams/{ref}/list_projects` ✅
### `GET /v1/teams/{ref}/list_members` ✅
### `GET /v1/teams/{ref}/list_deployments` ✅

These (plus `GET /v1/teams` and `GET /v1/projects/{id}/list_deployments`) are
**bounded** lists. The response shape is still a bare JSON array (matches
Cloud's `list_*` endpoints — no breaking change for existing tools), but the
server caps each page and signals continuation via a header:

- Query `?limit=N` (default 100, max 500). Negative / non-numeric is 400
  `invalid_limit`.
- Query `?cursor=<id>` to fetch the page after the row with that id. The
  cursor must refer to a row the caller can see (a team they're a member of,
  a project in that team, etc); a bogus cursor returns 400 `invalid_cursor`.
- Response header `X-Next-Cursor: <id>` is set when more rows exist after
  this page. Absent header = end of results.

Walk pattern (shell):

```bash
NEXT=""
while :; do
  RESP=$(curl -sfD - "http://localhost:8080/v1/teams${NEXT:+?cursor=$NEXT}" \
    -H "Authorization: Bearer $TOKEN")
  echo "$RESP" | sed -n '/^\r$/,$p' | tail -n +2 | jq .
  NEXT=$(echo "$RESP" | tr -d '\r' | awk -F': ' '/^X-Next-Cursor/ {print $2}')
  [ -z "$NEXT" ] && break
done
```

### `POST /v1/teams/{ref}/create_project` ✅

Body: `{projectName, deploymentType?, deploymentClass?, deploymentRegion?}`.
Returns `{projectId, projectSlug, project}`.

### `POST /v1/teams/{ref}/invite_team_member` ✅ (admins only)

Body: `{email, role}`. Returns `{inviteId, inviteToken, email, role}`. The
token is opaque; share it with the invitee out-of-band.

### `GET /v1/teams/{ref}/invites` 🔧 (admins only)

Lists pending (not-yet-accepted) invites — `[{id, email, role, token, invitedBy, createTime}]`.
Tokens are sensitive: anyone who has one can join the team.

### `POST /v1/teams/{ref}/invites/{inviteID}/cancel` 🔧 (admins only)

Deletes a pending invite. 404 if it was already accepted or never existed.

### `GET /v1/teams/{ref}/audit_log` ✅ (admins only)

Lists audit events for the team, newest first. Admin-only — audit data is
privileged. Members get 403 (matches Cloud's behavior; auditing is a
trust-anchor function).

Query params:
- `limit` (default 50, max 200) — page size.
- `cursor` — opaque continuation token returned as `nextCursor` from the
  previous page.

Response (200):

```json
{
  "items": [
    {
      "id": "12",
      "createTime": "2026-04-29T12:00:00Z",
      "action": "createProject",
      "actorId": "…",
      "actorEmail": "ian@example.com",
      "targetType": "project",
      "targetId": "…",
      "metadata": { "name": "my-app", "slug": "my-app" }
    }
  ],
  "nextCursor": "…"
}
```

Action names mirror Cloud's `auditLogActions` vocabulary where it exists:
`createTeam`, `inviteTeamMember`, `cancelInvite`, `createProject`,
`deleteProject`, `renameProject`, `updateProjectEnvVars`, `createDeployment`,
`deleteDeployment`, `acceptInvite`, `login`. Synapse-specific extensions
(no Cloud counterpart): `createPersonalAccessToken`,
`deletePersonalAccessToken`. Audit writes are best-effort: a transient DB
error during the audit insert never fails the user-visible request.

### `POST /v1/team_invites/accept` 🔧

Body: `{token}`. The caller must be authenticated. Adds the user as a
member with the role recorded in the invite, marks the invite consumed,
and returns `{teamId, teamSlug, teamName, role}`. Idempotent on the
membership insert (re-accepting from a second tab is a no-op).

## Projects

### `GET /v1/projects/{id}` ✅
### `PUT /v1/projects/{id}` 📍 (admins only) — body `{name?}`
### `POST /v1/projects/{id}/delete` ✅ (admins only)
### `GET /v1/projects/{id}/list_deployments` ✅
### `GET /v1/projects/{id}/list_default_environment_variables` ✅
### `POST /v1/projects/{id}/update_default_environment_variables` ✅ (admins only)

Body: `{changes: [{op:"set"|"delete", name, value?, deploymentTypes?}]}`.

## Deployments

### `POST /v1/projects/{id}/create_deployment` ✅ (admins only)

Body: `{type:"dev"|"prod"|"preview"|"custom", reference?, isDefault?}`.
Allocates a name, picks a free host port from the configured range,
provisions a Convex backend container via Docker, and returns the
`Deployment` row once `/version` responds (or after a 60s healthcheck
warning, whichever comes first).

### `GET /v1/projects/{id}/deployment` ✅

Find one deployment in this project. Query params:
- `reference=<string>` — match by `reference` field
- `defaultProd=true` — most recent production deployment marked default
- `defaultDev=true` — same for dev

Without query params, returns the newest non-deleted deployment.

### `GET /v1/deployments/{name}` ✅
### `POST /v1/deployments/{name}/delete` ✅ (admins only)

Stops + removes the container, drops its data volume, marks the row deleted.

### `GET /v1/deployments/{name}/auth` 🔧 (members only)

Returns `{deploymentName, deploymentUrl, adminKey, deploymentType}`. The
dashboard calls this when the user clicks **Open** to launch the standalone
Convex dashboard against this deployment.

### `GET /v1/deployments/{name}/cli_credentials` 🔧 (members only)

Returns the env-var pair the [Convex CLI](https://www.npmjs.com/package/convex)
looks for when running against a self-hosted backend, plus a copy-pastable
shell snippet that sets both at once:

```json
{
  "deploymentName": "happy-cat-1234",
  "convexUrl": "http://127.0.0.1:3211",
  "adminKey": "…",
  "exportSnippet": "export CONVEX_SELF_HOSTED_URL='http://127.0.0.1:3211'\nexport CONVEX_SELF_HOSTED_ADMIN_KEY='…'"
}
```

The CLI's deployment-selection logic (in
[`lib/deploymentSelection.ts`](https://github.com/get-convex/convex-backend/blob/main/npm-packages/convex/src/cli/lib/deploymentSelection.ts))
treats the presence of both `CONVEX_SELF_HOSTED_URL` and
`CONVEX_SELF_HOSTED_ADMIN_KEY` as the "selfHosted" path and skips Big Brain
entirely. `CONVEX_DEPLOYMENT` must NOT also be set in that mode.

Quickstart:

```bash
# Get credentials for a deployment (JWT or PAT both work)
eval "$(curl -sf http://localhost:8080/v1/deployments/<NAME>/cli_credentials \
        -H "Authorization: Bearer $TOKEN" \
      | python3 -c 'import sys,json; print(json.load(sys.stdin)["exportSnippet"])')"

# Now the CLI talks straight to the Synapse-managed backend container
npx convex dev --once
npx convex deploy
```

### `POST /v1/deployments/{name}/create_deploy_key` ✅ (admins only)

Body: `{name?}`. Returns `{id, name, token}`. Token is shown ONCE — store it.

## Reverse proxy

When `SYNAPSE_PROXY_ENABLED=true`, the API server also serves
`/d/{deploymentName}/*`, forwarding the rest of the path to the
provisioned Convex backend. Lets you expose a single host port (8080)
instead of one per deployment.

Example:

```
http://localhost:8080/d/quiet-cat-1234/api/check_admin_key
       │              │                │
       │              │                └─ forwarded as /api/check_admin_key
       │              └─ deployment name
       └─ Synapse host
```

No auth check at the proxy layer — deployments enforce admin-key auth
themselves.

## Personal access tokens

User-scoped opaque tokens for CLI / CI / programmatic access. The plaintext
token is shown ONCE at creation; the server stores only its SHA-256 hash
and cannot recover the original. All three endpoints require an
authenticated caller (JWT or a previously-issued PAT) and only operate on
tokens belonging to the caller.

### `POST /v1/create_personal_access_token` ✅

Body:

```json
{
  "name": "ci-runner",
  "scope": "user",
  "scopeId": null,
  "expiresAt": null
}
```

- `name` (required) — short label, ≤100 chars.
- `scope` (default `"user"`) — one of `user`, `team`, `project`, `deployment`.
- `scopeId` — required when `scope` is not `"user"`; the UUID of the
  team/project/deployment the token is bound to.
- `expiresAt` — optional ISO-8601 timestamp; must be in the future. Omit
  for a non-expiring token.

Response (201):

```json
{
  "token": "syn_abc123…",
  "accessToken": {
    "id": "…",
    "name": "ci-runner",
    "scope": "user",
    "createTime": "2026-04-29T12:00:00Z"
  }
}
```

The plaintext `token` is the value to send as `Authorization: Bearer …`
on subsequent requests. Save it immediately — it is never returned again.

### `GET /v1/list_personal_access_tokens` ✅

Lists tokens belonging to the caller, newest first.

Query params:
- `limit` (default 50, max 200) — page size.
- `cursor` — opaque continuation token returned as `nextCursor` from the
  previous page. Must refer to a token the caller owns.

Response (200):

```json
{
  "items": [
    {
      "id": "…",
      "name": "ci-runner",
      "scope": "user",
      "createTime": "2026-04-29T12:00:00Z",
      "lastUsedAt": "2026-04-29T12:05:00Z"
    }
  ],
  "nextCursor": "…"
}
```

`nextCursor` is omitted on the last page. Token hashes and plaintext
tokens are NEVER included.

### `POST /v1/delete_personal_access_token` ✅

Body: `{"id": "<token-uuid>"}`. Hard-deletes the token if it belongs to
the caller. Returns `{"id": "…"}` on success, `404 token_not_found`
otherwise. Subsequent auth attempts with that token will be rejected by
the auth middleware.

## Errors

All errors return `{code, message}` with an HTTP status. Codes are stable;
messages may evolve.

| code | typical status | meaning |
|---|---|---|
| `bad_request` | 400 | malformed JSON / unknown field |
| `missing_*` | 400 | required field omitted |
| `invalid_*` | 400 | field present but not valid |
| `unauthorized` | 401 | missing or expired bearer |
| `invalid_token` | 401 | token signature/expiry/kind wrong |
| `invalid_credentials` | 401 | login email/password mismatch |
| `forbidden` | 403 | authenticated but not allowed |
| `*_not_found` | 404 | target doesn't exist (or you can't see it) |
| `email_taken` | 409 | unique constraint on registration |
| `internal` | 500 | server bug — check logs |
