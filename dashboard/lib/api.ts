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
  deploymentType?: "dev" | "prod";
  status?: string;
  url?: string;
  deploymentUrl?: string;
  createdAt?: string;
  isDefault?: boolean;
};

export type DeploymentAuth = {
  deploymentName: string;
  deploymentType: "dev" | "prod";
  adminKey: string;
  deploymentUrl: string;
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
  scope: "user" | "team" | "project" | "deployment";
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
      body: { type: "dev" | "prod"; isDefault?: boolean }
    ): Promise<Deployment> {
      return request<Deployment>(
        `/v1/projects/${encodeURIComponent(id)}/create_deployment`,
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
    // Returns deploymentUrl + adminKey for talking to the backend directly.
    // Mirrors Convex Cloud's /api/dashboard/instances/{name}/auth.
    auth(name: string): Promise<DeploymentAuth> {
      return request<DeploymentAuth>(
        `/v1/deployments/${encodeURIComponent(name)}/auth`
      );
    },
    delete(name: string): Promise<void> {
      return request<void>(`/v1/deployments/${encodeURIComponent(name)}/delete`, {
        method: "POST",
        body: {},
      });
    },
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
};

export { ApiError };
