# API reference

Synapse implements (a subset of) Convex Cloud's stable
[Management API v1](https://github.com/get-convex/convex-backend/blob/main/npm-packages/dashboard/dashboard-management-openapi.json).
Endpoints below are grouped by resource. Compatibility with the OpenAPI spec
is noted as ✅ (matches), 🔧 (custom — Synapse extension), or 📍 (Cloud-style
endpoint with a smaller payload).

All authenticated endpoints expect `Authorization: Bearer <token>` where the
token is either:
- A JWT issued by `/v1/auth/login` (15-minute lifetime by default), or
- A `syn_*` opaque personal-access token (created via the dashboard or, in
  v0.2+, via `/v1/create_personal_access_token`).

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

### `POST /v1/teams/{ref}/create_project` ✅

Body: `{projectName, deploymentType?, deploymentClass?, deploymentRegion?}`.
Returns `{projectId, projectSlug, project}`.

### `POST /v1/teams/{ref}/invite_team_member` ✅ (admins only)

Body: `{email, role}`. Returns `{inviteId, inviteToken, email, role}`. The
token is opaque; share it with the invitee out-of-band.

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

### `POST /v1/deployments/{name}/create_deploy_key` ✅ (admins only)

Body: `{name?}`. Returns `{id, name, token}`. Token is shown ONCE — store it.

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
