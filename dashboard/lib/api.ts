// Typed wrapper around Synapse's REST surface. All authenticated calls
// pull the bearer from localStorage; on 401 we wipe state and redirect.

import { clearAuth, getAccessToken, saveAuth, type AuthBundle, type User } from "./auth";

const BASE_URL =
  process.env.NEXT_PUBLIC_SYNAPSE_URL?.replace(/\/$/, "") || "http://localhost:8080";

export type Team = {
  id: string;
  name: string;
  slug: string;
  creatorId?: string;
  createdAt?: string;
  // defaultRegion is stored verbatim and round-tripped on update_team —
  // Synapse self-hosted is single-region so this is informational. Cloud
  // uses it to drive the dashboard's region picker.
  defaultRegion?: string;
};

export type TeamMember = {
  id: string;
  teamId: string;
  email: string;
  name: string;
  role: "admin" | "member";
  createTime: string;
};

// Project-level RBAC member view. role widens to include "viewer"
// (project-only). source records whether the role came from the
// project_members override or fell through to the team_members table.
// Lets the dashboard show "team admin (project viewer)" without a
// second roundtrip.
export type ProjectMember = {
  id: string;
  email: string;
  name: string;
  role: "admin" | "member" | "viewer";
  source: "project" | "team";
  createTime: string;
};

export type Project = {
  id: string;
  teamId: string;
  name: string;
  slug: string;
  createdAt?: string;
};

export type Deployment = {
  id?: string;
  name: string;
  projectId?: string;
  type?: "dev" | "prod";
  deploymentType?: "dev" | "prod" | "preview" | "custom";
  status?: string;
  url?: string;
  deploymentUrl?: string;
  createdAt?: string;
  isDefault?: boolean;
  // Operator-supplied label, e.g. a git ref or a CI run id. The
  // server returns whatever was set on /create_deployment; the
  // picker indexes it for search.
  reference?: string;
  // True for deployments registered via /adopt_deployment — Synapse points
  // at an external Convex backend rather than a container it provisioned.
  adopted?: boolean;
  // HA flags (v0.5+). Both are absent on legacy / single-replica deployments;
  // present + true means the row is backed by Postgres + S3 with N replicas.
  haEnabled?: boolean;
  replicaCount?: number;
};

// Body shape for POST /v1/projects/{id}/adopt_deployment.
export type AdoptDeploymentInput = {
  deploymentUrl: string;
  adminKey: string;
  deploymentType?: "dev" | "prod" | "preview" | "custom";
  name?: string;
  isDefault?: boolean;
  reference?: string;
};

export type DeploymentAuth = {
  deploymentName: string;
  deploymentType: "dev" | "prod";
  adminKey: string;
  deploymentUrl: string;
};

// Returned by GET /v1/deployments/{name}/cli_credentials. The CLI consumes
// CONVEX_SELF_HOSTED_URL + CONVEX_SELF_HOSTED_ADMIN_KEY (see Convex CLI's
// `lib/utils/utils.ts`); `envSnippet` is a `.env.local`-pasteable form,
// `exportSnippet` is the same values prefixed with `export ` for shells.
export type CliCredentials = {
  deploymentName: string;
  convexUrl: string;
  adminKey: string;
  exportSnippet: string;
  envSnippet: string;
};

// DeployKey mirrors models.DeployKey on the backend. `adminKey` is only
// populated on the create response (operator sees it once); subsequent
// list responses see only the `prefix` chip.
export type DeployKey = {
  id: string;
  deploymentId: string;
  name: string;
  adminKey?: string;
  prefix: string;
  createdBy?: string;
  createdByName?: string;
  createTime: string;
  lastUsedAt?: string;
  revokedAt?: string;
};

// Returned by GET /v1/admin/version_check. `current` is always populated.
// `latest` and the surrounding fields are filled when we successfully
// reached the GitHub release stream (cached up to 15min); on failure
// `error` carries a short reason and the dashboard renders a degraded
// banner instead of suggesting the operator upgrade.
export type VersionCheck = {
  current: string;
  latest?: string;
  updateAvailable: boolean;
  releaseUrl?: string;
  releaseNotes?: string;
  publishedAt?: string;
  fetchedAt?: string;
  error?: string;
};

// POST /v1/admin/upgrade kicks off the host-side daemon. The real action
// is async — the response just confirms the daemon accepted the request;
// poll /v1/admin/upgrade/status for the actual progress.
export type UpgradeResponse = {
  started: boolean;
  ref: string;
};

// GET /v1/admin/upgrade/status. State transitions:
//   idle    → no upgrade has run since the daemon started
//   running → setup.sh --upgrade is executing right now
//   success → last run finished with exit 0
//   failed  → last run exited non-zero
//   unavailable → daemon socket missing (non-systemd host or not yet installed)
export type UpgradeStatus = {
  state: "idle" | "running" | "success" | "failed" | "unavailable";
  ref?: string;
  startedAt?: string;
  finishedAt?: string;
  exitCode?: number;
  logTail?: string[];
  logPath?: string;
  error?: string;
};

// Returned once at create time. `adminKey` is the freshly-minted value;
// `envSnippet` and `exportSnippet` are paste-ready for `.env.local` and
// shell respectively. The dashboard MUST surface this immediately and
// not store it — Synapse keeps only the prefix + sha256 hash going
// forward (GitHub-PAT model).
export type CreateDeployKeyResponse = DeployKey & {
  envSnippet: string;
  exportSnippet: string;
};

// DeploymentDomain mirrors models.DeploymentDomain on the backend
// (synapse/internal/models/models.go). A custom domain registered against
// a deployment so callers can reach it at e.g. "api.example.com" instead
// of the "<name>.<base>" wildcard subdomain. The proxy + on-demand TLS
// layer (PR #5) reads rows where status='active' for routing decisions;
// this DTO is what the dashboard consumes from /v1/deployments/{name}/domains.
//
// status='pending' (just inserted, DNS not yet verified or
// SYNAPSE_PUBLIC_IP unconfigured), 'active' (A record matches the
// configured public IP), 'failed' (lookup error or wrong IP — see
// lastDnsError for details).
export type DeploymentDomain = {
  id: string;
  deploymentId: string;
  domain: string;
  role: "api" | "dashboard";
  status: "pending" | "active" | "failed";
  dnsVerifiedAt?: string;
  lastDnsError?: string;
  createdAt: string;
  updatedAt: string;
};

export type EnvVar = {
  name: string;
  value: string;
  deploymentTypes: string[];
};

export type EnvVarChange =
  | { op: "set"; name: string; value: string; deploymentTypes?: string[] }
  | { op: "delete"; name: string };

export type PendingInvite = {
  id: string;
  email: string;
  role: "admin" | "member";
  token: string;
  invitedBy: string;
  createTime: string;
};

export type AccessToken = {
  id: string;
  name: string;
  // 'app' is a project-scope variant used for preview deploy keys (CI/CD).
  // Same access surface as 'project' but listed in a separate panel so
  // operators can categorise long-lived service tokens vs short-lived
  // preview keys.
  scope: "user" | "team" | "project" | "app" | "deployment";
  scopeId?: string;
  createTime: string;
  expiresAt?: string;
  lastUsedAt?: string;
};

export type CreateTokenResponse = {
  // Plaintext "syn_*" string — shown ONCE at creation; never returned again.
  token: string;
  accessToken: AccessToken;
};

export type CreateTokenOpts = {
  scope?: AccessToken["scope"];
  scopeId?: string;
  expiresAt?: string;
};

export type ListTokensResponse = {
  items: AccessToken[];
  nextCursor?: string;
};

// AuditEvent maps GET /v1/teams/{ref}/audit_log items. Action names mirror
// the convex-backend audit-log vocabulary (createTeam, createProject, etc.)
// — see synapse/internal/audit/audit.go for the canonical list. The id is a
// stringified BIGSERIAL; opaque to clients.
export type AuditEvent = {
  id: string;
  createTime: string;
  action: string;
  actorId?: string;
  actorEmail?: string;
  targetType?: string;
  targetId?: string;
  metadata?: Record<string, unknown>;
};

export type ListAuditLogResponse = {
  items: AuditEvent[];
  nextCursor?: string;
};

class ApiError extends Error {
  status: number;
  code?: string;
  constructor(status: number, code: string | undefined, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

type RequestOpts = {
  method?: string;
  body?: unknown;
  auth?: boolean; // include bearer; default true
};

async function request<T>(path: string, opts: RequestOpts = {}): Promise<T> {
  const { method = "GET", body, auth = true } = opts;
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (auth) {
    const token = getAccessToken();
    if (token) headers["Authorization"] = `Bearer ${token}`;
  }

  let res: Response;
  try {
    res = await fetch(`${BASE_URL}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      cache: "no-store",
    });
  } catch (err) {
    throw new ApiError(0, "network_error", (err as Error).message);
  }

  if (res.status === 401 && auth) {
    clearAuth();
    if (typeof window !== "undefined" && !window.location.pathname.startsWith("/login")) {
      window.location.href = "/login";
    }
    throw new ApiError(401, "unauthorized", "Session expired");
  }

  if (res.status === 204) return undefined as T;

  let json: unknown = null;
  const text = await res.text();
  if (text) {
    try {
      json = JSON.parse(text);
    } catch {
      // fallthrough
    }
  }

  if (!res.ok) {
    const j = (json ?? {}) as { code?: string; message?: string };
    throw new ApiError(res.status, j.code, j.message || res.statusText);
  }
  return (json as T) ?? (undefined as T);
}

type TokenResponse = {
  accessToken: string;
  refreshToken: string;
  tokenType?: string;
  expiresIn?: number;
  user: User;
};

function persistToken(t: TokenResponse): AuthBundle {
  const bundle: AuthBundle = {
    accessToken: t.accessToken,
    refreshToken: t.refreshToken,
    user: t.user,
  };
  saveAuth(bundle);
  return bundle;
}

export const api = {
  async register(email: string, password: string, name?: string): Promise<AuthBundle> {
    const t = await request<TokenResponse>("/v1/auth/register", {
      method: "POST",
      body: { email, password, name },
      auth: false,
    });
    return persistToken(t);
  },

  async login(email: string, password: string): Promise<AuthBundle> {
    const t = await request<TokenResponse>("/v1/auth/login", {
      method: "POST",
      body: { email, password },
      auth: false,
    });
    return persistToken(t);
  },

  me(): Promise<User> {
    return request<User>("/v1/me");
  },

  // Profile mutations. Both endpoints are also exposed under /v1/me/...; we
  // hit the top-level cloud-spec routes so a future drop-in replacement of
  // the dashboard against another self-hosted Convex backend works without
  // changing client code.
  updateProfileName(name: string): Promise<User> {
    return request<User>("/v1/update_profile_name", {
      method: "PUT",
      body: { name },
    });
  },
  deleteAccount(): Promise<{ id: string; status: string }> {
    return request<{ id: string; status: string }>("/v1/delete_account", {
      method: "POST",
      body: {},
    });
  },

  teams: {
    async list(): Promise<Team[]> {
      // Endpoint may return either an array or {teams: [...]}.
      const r = await request<Team[] | { teams: Team[] }>("/v1/teams");
      return Array.isArray(r) ? r : r.teams ?? [];
    },
    create(name: string): Promise<Team> {
      return request<Team>("/v1/teams/create_team", {
        method: "POST",
        body: { name },
      });
    },
    get(ref: string): Promise<Team> {
      return request<Team>(`/v1/teams/${encodeURIComponent(ref)}`);
    },
    async listProjects(ref: string): Promise<Project[]> {
      const r = await request<Project[] | { projects: Project[] }>(
        `/v1/teams/${encodeURIComponent(ref)}/list_projects`
      );
      return Array.isArray(r) ? r : r.projects ?? [];
    },
    async listDeployments(ref: string): Promise<Deployment[]> {
      // Team-scoped flat list across every project — used by the home view's
      // "Deployments" tab. Mirrors per-project list_deployments shape.
      const r = await request<Deployment[] | { deployments: Deployment[] }>(
        `/v1/teams/${encodeURIComponent(ref)}/list_deployments`
      );
      return Array.isArray(r) ? r : r.deployments ?? [];
    },
    listMembers(ref: string): Promise<TeamMember[]> {
      return request<TeamMember[]>(`/v1/teams/${encodeURIComponent(ref)}/list_members`);
    },
    createProject(
      ref: string,
      name: string
    ): Promise<{ projectId: string; projectSlug: string; project: Project }> {
      return request(`/v1/teams/${encodeURIComponent(ref)}/create_project`, {
        method: "POST",
        body: { projectName: name },
      });
    },
    invite(
      ref: string,
      email: string,
      role: "admin" | "member" = "member"
    ): Promise<{ inviteId: string; inviteToken: string; email: string; role: string }> {
      return request(`/v1/teams/${encodeURIComponent(ref)}/invite_team_member`, {
        method: "POST",
        body: { email, role },
      });
    },
    listInvites(ref: string): Promise<PendingInvite[]> {
      return request<PendingInvite[]>(`/v1/teams/${encodeURIComponent(ref)}/invites`);
    },
    cancelInvite(ref: string, inviteId: string): Promise<void> {
      return request<void>(
        `/v1/teams/${encodeURIComponent(ref)}/invites/${encodeURIComponent(inviteId)}/cancel`,
        { method: "POST", body: {} },
      );
    },
    // POST /v1/teams/{ref} — update team name / slug / defaultRegion.
    // All fields optional. Returns 200 with the refreshed Team.
    update(
      ref: string,
      patch: { name?: string; slug?: string; defaultRegion?: string },
    ): Promise<Team> {
      return request<Team>(`/v1/teams/${encodeURIComponent(ref)}`, {
        method: "POST",
        body: patch,
      });
    },
    // POST /v1/teams/{ref}/delete — admin-only, refuses 409 team_has_deployments.
    delete(ref: string): Promise<{ id: string; status: string }> {
      return request<{ id: string; status: string }>(
        `/v1/teams/${encodeURIComponent(ref)}/delete`,
        { method: "POST", body: {} },
      );
    },
    // POST /v1/teams/{ref}/update_member_role — admin/member; the cloud
    // alias 'developer' is also accepted server-side (mapped → member).
    updateMemberRole(
      ref: string,
      memberId: string,
      role: "admin" | "member" | "developer",
    ): Promise<{ memberId: string; role: string }> {
      return request<{ memberId: string; role: string }>(
        `/v1/teams/${encodeURIComponent(ref)}/update_member_role`,
        { method: "POST", body: { memberId, role } },
      );
    },
    // POST /v1/teams/{ref}/remove_member — admin or self-removal.
    removeMember(
      ref: string,
      memberId: string,
    ): Promise<{ memberId: string; status: string }> {
      return request<{ memberId: string; status: string }>(
        `/v1/teams/${encodeURIComponent(ref)}/remove_member`,
        { method: "POST", body: { memberId } },
      );
    },
    // Team-scoped access tokens. Issued tokens carry scope=team + scope_id=<this team>.
    listTokens(
      ref: string,
      opts: { cursor?: string; limit?: number } = {},
    ): Promise<ListTokensResponse> {
      const params = new URLSearchParams();
      if (opts.cursor) params.set("cursor", opts.cursor);
      if (opts.limit !== undefined) params.set("limit", String(opts.limit));
      const qs = params.toString();
      return request<ListTokensResponse>(
        `/v1/teams/${encodeURIComponent(ref)}/access_tokens${qs ? `?${qs}` : ""}`,
      );
    },
    createToken(
      ref: string,
      name: string,
      opts: { expiresAt?: string } = {},
    ): Promise<CreateTokenResponse> {
      return request<CreateTokenResponse>(
        `/v1/teams/${encodeURIComponent(ref)}/access_tokens`,
        { method: "POST", body: { name, ...opts } },
      );
    },
    auditLog(
      ref: string,
      opts: { cursor?: string; limit?: number } = {},
    ): Promise<ListAuditLogResponse> {
      const params = new URLSearchParams();
      if (opts.cursor) params.set("cursor", opts.cursor);
      if (opts.limit !== undefined) params.set("limit", String(opts.limit));
      const qs = params.toString();
      return request<ListAuditLogResponse>(
        `/v1/teams/${encodeURIComponent(ref)}/audit_log${qs ? `?${qs}` : ""}`,
      );
    },
  },

  invites: {
    accept(token: string): Promise<{ teamId: string; teamSlug: string; teamName: string; role: string }> {
      return request("/v1/team_invites/accept", {
        method: "POST",
        body: { token },
      });
    },
  },

  projects: {
    get(id: string): Promise<Project> {
      return request<Project>(`/v1/projects/${encodeURIComponent(id)}`);
    },
    async listDeployments(id: string): Promise<Deployment[]> {
      const r = await request<Deployment[] | { deployments: Deployment[] }>(
        `/v1/projects/${encodeURIComponent(id)}/list_deployments`
      );
      return Array.isArray(r) ? r : r.deployments ?? [];
    },
    createDeployment(
      id: string,
      body: {
        type: "dev" | "prod";
        isDefault?: boolean;
        // HA mode (v0.5+). When true, the backend provisions 2 replicas
        // backed by Postgres + S3. Refused with 400 ha_disabled when
        // SYNAPSE_HA_ENABLED is not set on the cluster.
        ha?: boolean;
      }
    ): Promise<Deployment> {
      return request<Deployment>(
        `/v1/projects/${encodeURIComponent(id)}/create_deployment`,
        { method: "POST", body }
      );
    },
    // Register an existing Convex backend (running outside Synapse) as a
    // deployment under this project. The backend stores the URL + admin key
    // and skips Docker calls in delete / health flows.
    adoptDeployment(
      id: string,
      body: AdoptDeploymentInput
    ): Promise<Deployment> {
      return request<Deployment>(
        `/v1/projects/${encodeURIComponent(id)}/adopt_deployment`,
        { method: "POST", body }
      );
    },
    delete(id: string): Promise<void> {
      return request<void>(`/v1/projects/${encodeURIComponent(id)}/delete`, {
        method: "POST",
        body: {},
      });
    },
    rename(id: string, name: string): Promise<Project> {
      return request<Project>(`/v1/projects/${encodeURIComponent(id)}`, {
        method: "PUT",
        body: { name },
      });
    },
    // PUT /v1/projects/{id} — accepts any subset of {name, slug}. Slug
    // shape is enforced server-side (lowercase letters / digits / dashes);
    // collisions return 409 slug_taken.
    update(
      id: string,
      patch: { name?: string; slug?: string },
    ): Promise<Project> {
      return request<Project>(`/v1/projects/${encodeURIComponent(id)}`, {
        method: "PUT",
        body: patch,
      });
    },
    // POST /v1/projects/{id}/transfer — admin of source AND destination team.
    // 204 on success; we map that to void here.
    transfer(id: string, destinationTeamId: string): Promise<void> {
      return request<void>(`/v1/projects/${encodeURIComponent(id)}/transfer`, {
        method: "POST",
        body: { destinationTeamId },
      });
    },
    // ---------- Project members (v1.0+ RBAC) ----------
    // Lists everyone with access to the project — team members merged
    // with project_members overrides. The role on each row is the
    // EFFECTIVE role (override > team fallback); the `source` field
    // says which side it came from.
    listMembers(id: string): Promise<ProjectMember[]> {
      return request<ProjectMember[]>(
        `/v1/projects/${encodeURIComponent(id)}/list_members`,
      );
    },
    // Admin-only. Target user must already be a member of the
    // project's team — the team is the trust boundary, projects are
    // partitions of it. role accepts admin / member / viewer.
    addMember(
      id: string,
      userId: string,
      role: "admin" | "member" | "viewer",
    ): Promise<{ projectId: string; userId: string; role: string }> {
      return request(`/v1/projects/${encodeURIComponent(id)}/add_member`, {
        method: "POST",
        body: { userId, role },
      });
    },
    // Admin-only. Upserts the project_members row — same effect as
    // add_member when there's no override yet.
    updateMemberRole(
      id: string,
      memberId: string,
      role: "admin" | "member" | "viewer",
    ): Promise<{ memberId: string; role: string }> {
      return request(
        `/v1/projects/${encodeURIComponent(id)}/update_member_role`,
        { method: "POST", body: { memberId, role } },
      );
    },
    // Admin OR self. Removes the override row; the user falls back to
    // their team_members role for this project. Returns 404
    // no_override when nothing was there.
    removeMember(
      id: string,
      memberId: string,
    ): Promise<{ memberId: string; status: string }> {
      return request(
        `/v1/projects/${encodeURIComponent(id)}/remove_member`,
        { method: "POST", body: { memberId } },
      );
    },
    // Project-scoped tokens (scope=project). Issued tokens can act on this
    // project + its deployments; team-level operations 403.
    listTokens(
      id: string,
      opts: { cursor?: string; limit?: number } = {},
    ): Promise<ListTokensResponse> {
      const params = new URLSearchParams();
      if (opts.cursor) params.set("cursor", opts.cursor);
      if (opts.limit !== undefined) params.set("limit", String(opts.limit));
      const qs = params.toString();
      return request<ListTokensResponse>(
        `/v1/projects/${encodeURIComponent(id)}/access_tokens${qs ? `?${qs}` : ""}`,
      );
    },
    createToken(
      id: string,
      name: string,
      opts: { expiresAt?: string } = {},
    ): Promise<CreateTokenResponse> {
      return request<CreateTokenResponse>(
        `/v1/projects/${encodeURIComponent(id)}/access_tokens`,
        { method: "POST", body: { name, ...opts } },
      );
    },
    // App tokens (scope=app). Same access surface as project tokens but
    // displayed separately so operators can label "preview deploy keys"
    // (CI/CD short-lived) apart from regular project tokens.
    listAppTokens(
      id: string,
      opts: { cursor?: string; limit?: number } = {},
    ): Promise<ListTokensResponse> {
      const params = new URLSearchParams();
      if (opts.cursor) params.set("cursor", opts.cursor);
      if (opts.limit !== undefined) params.set("limit", String(opts.limit));
      const qs = params.toString();
      return request<ListTokensResponse>(
        `/v1/projects/${encodeURIComponent(id)}/app_access_tokens${qs ? `?${qs}` : ""}`,
      );
    },
    createAppToken(
      id: string,
      name: string,
      opts: { expiresAt?: string } = {},
    ): Promise<CreateTokenResponse> {
      return request<CreateTokenResponse>(
        `/v1/projects/${encodeURIComponent(id)}/app_access_tokens`,
        { method: "POST", body: { name, ...opts } },
      );
    },
    async listEnvVars(id: string): Promise<EnvVar[]> {
      const r = await request<{ configs: EnvVar[] }>(
        `/v1/projects/${encodeURIComponent(id)}/list_default_environment_variables`
      );
      return r.configs ?? [];
    },
    updateEnvVars(
      id: string,
      changes: EnvVarChange[]
    ): Promise<{ applied: number }> {
      return request(
        `/v1/projects/${encodeURIComponent(id)}/update_default_environment_variables`,
        { method: "POST", body: { changes } }
      );
    },
  },

  deployments: {
    // Returns the full Deployment row (projectId, type, status, etc).
    // Used by the /embed/<name> picker to discover the project a
    // deployment belongs to before listing its siblings.
    get(name: string): Promise<Deployment> {
      return request<Deployment>(
        `/v1/deployments/${encodeURIComponent(name)}`
      );
    },
    // Returns deploymentUrl + adminKey for talking to the backend directly.
    // Mirrors Convex Cloud's /api/dashboard/instances/{name}/auth.
    auth(name: string): Promise<DeploymentAuth> {
      return request<DeploymentAuth>(
        `/v1/deployments/${encodeURIComponent(name)}/auth`
      );
    },
    // Same auth gate as `auth`, but shaped for `npx convex` consumption: the
    // env-var pair the CLI looks for plus a copy-paste shell snippet.
    cliCredentials(name: string): Promise<CliCredentials> {
      return request<CliCredentials>(
        `/v1/deployments/${encodeURIComponent(name)}/cli_credentials`
      );
    },
    delete(name: string): Promise<void> {
      return request<void>(`/v1/deployments/${encodeURIComponent(name)}/delete`, {
        method: "POST",
        body: {},
      });
    },
    // Deployment-scoped tokens (scope=deployment). Strictest scope —
    // bearer can only act on this exact deployment.
    listTokens(
      name: string,
      opts: { cursor?: string; limit?: number } = {},
    ): Promise<ListTokensResponse> {
      const params = new URLSearchParams();
      if (opts.cursor) params.set("cursor", opts.cursor);
      if (opts.limit !== undefined) params.set("limit", String(opts.limit));
      const qs = params.toString();
      return request<ListTokensResponse>(
        `/v1/deployments/${encodeURIComponent(name)}/access_tokens${qs ? `?${qs}` : ""}`,
      );
    },
    createToken(
      name: string,
      tokenName: string,
      opts: { expiresAt?: string } = {},
    ): Promise<CreateTokenResponse> {
      return request<CreateTokenResponse>(
        `/v1/deployments/${encodeURIComponent(name)}/access_tokens`,
        { method: "POST", body: { name: tokenName, ...opts } },
      );
    },
    // Deploy keys (v1.0.3+). Per-deployment named admin keys for CI
    // integrations. Mirrors Convex Cloud's "Personal Deployment
    // Settings → Deploy Keys" surface.
    listDeployKeys(name: string): Promise<{ deployKeys: DeployKey[] }> {
      return request<{ deployKeys: DeployKey[] }>(
        `/v1/deployments/${encodeURIComponent(name)}/deploy_keys`,
      );
    },
    createDeployKey(
      name: string,
      keyName: string,
    ): Promise<CreateDeployKeyResponse> {
      return request<CreateDeployKeyResponse>(
        `/v1/deployments/${encodeURIComponent(name)}/deploy_keys`,
        { method: "POST", body: { name: keyName } },
      );
    },
    revokeDeployKey(name: string, keyId: string): Promise<void> {
      return request<void>(
        `/v1/deployments/${encodeURIComponent(name)}/deploy_keys/${encodeURIComponent(keyId)}/revoke`,
        { method: "POST", body: {} },
      );
    },
    // Custom domains (PR #64+). Per-deployment domain registry — POST
    // triggers a synchronous DNS preflight against SYNAPSE_PUBLIC_IP, so
    // the row often comes back already 'active' or 'failed'. List wraps
    // the {domains:[…]} envelope so callers always get a flat array.
    async listDomains(name: string): Promise<DeploymentDomain[]> {
      const r = await request<{ domains: DeploymentDomain[] }>(
        `/v1/deployments/${encodeURIComponent(name)}/domains`,
      );
      return r.domains ?? [];
    },
    addDomain(
      name: string,
      domain: string,
      role: "api" | "dashboard",
    ): Promise<DeploymentDomain> {
      return request<DeploymentDomain>(
        `/v1/deployments/${encodeURIComponent(name)}/domains`,
        { method: "POST", body: { domain, role } },
      );
    },
    deleteDomain(name: string, domainId: string): Promise<void> {
      return request<void>(
        `/v1/deployments/${encodeURIComponent(name)}/domains/${encodeURIComponent(domainId)}`,
        { method: "DELETE" },
      );
    },
    verifyDomain(name: string, domainId: string): Promise<DeploymentDomain> {
      return request<DeploymentDomain>(
        `/v1/deployments/${encodeURIComponent(name)}/domains/${encodeURIComponent(domainId)}/verify`,
        { method: "POST", body: {} },
      );
    },
  },

  // First-run wizard probe. Public — no auth, hit pre-login. firstRun
  // is true iff the users table is empty (operator just bootstrapped
  // Synapse). Drives /login → /setup redirects.
  installStatus(): Promise<{ firstRun: boolean; version: string }> {
    return request<{ firstRun: boolean; version: string }>("/v1/install_status", {
      auth: false,
    });
  },

  // Personal access tokens. The plaintext token comes back ONCE in `create()` —
  // callers must surface it to the user immediately and stash it; the server
  // only stores a SHA-256 hash and can never recover the original.
  tokens: {
    create(name: string, opts: CreateTokenOpts = {}): Promise<CreateTokenResponse> {
      return request<CreateTokenResponse>("/v1/create_personal_access_token", {
        method: "POST",
        body: { name, ...opts },
      });
    },
    list(opts: { cursor?: string; limit?: number } = {}): Promise<ListTokensResponse> {
      const params = new URLSearchParams();
      if (opts.cursor) params.set("cursor", opts.cursor);
      if (opts.limit !== undefined) params.set("limit", String(opts.limit));
      const qs = params.toString();
      return request<ListTokensResponse>(
        `/v1/list_personal_access_tokens${qs ? `?${qs}` : ""}`,
      );
    },
    delete(id: string): Promise<{ id: string }> {
      return request<{ id: string }>("/v1/delete_personal_access_token", {
        method: "POST",
        body: { id },
      });
    },
  },

  // Instance-level admin operations: version_check + auto-upgrade.
  // Gated to "any team admin" — see synapse/internal/api/admin.go.
  admin: {
    versionCheck(): Promise<VersionCheck> {
      return request<VersionCheck>("/v1/admin/version_check");
    },
    upgrade(ref?: string): Promise<UpgradeResponse> {
      return request<UpgradeResponse>("/v1/admin/upgrade", {
        method: "POST",
        body: ref ? { ref } : {},
      });
    },
    upgradeStatus(): Promise<UpgradeStatus> {
      return request<UpgradeStatus>("/v1/admin/upgrade/status");
    },
  },
};

export { ApiError };
