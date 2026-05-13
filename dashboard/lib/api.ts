// Typed wrapper around Synapse's REST surface. All authenticated calls
// pull the bearer from localStorage; on 401 we wipe state and redirect.

import { clearAuth, getAccessToken, saveAuth, type AuthBundle, type User } from "./auth";

// Pre-v1.6.11: BASE_URL was a build-time-baked constant pointing at
// the operator's main install URL (e.g. https://synapsepanel.com).
// That broke when v1.6.11+ shipped role='dashboard' custom domains:
// JS served from `dashboard.<your>.com` still called the baked URL,
// every /v1/* request went cross-origin, and the cookie / CORS
// story got hairy.
//
// v1.6.11+: resolve at call time, with a rule that handles both
// the "same hostname, different port" dev workflow AND the
// "completely different host" custom-domain case:
//
//   1. SSR / Node tests (window undefined)
//        → env var if set, else http://localhost:8080.
//
//   2. Browser, env var set, and the env URL's origin matches the
//      current page origin (or the same hostname on a different
//      port — dev's localhost:8080 vs localhost:6790)
//        → env URL. Keeps existing dev + production-main-domain
//          behaviour identical to pre-v1.6.11.
//
//   3. Browser, anything else (custom dashboard domain hits the
//      operator's main-URL-baked env)
//        → window.location.origin. Caddy + synapse-api route /v1/*
//          to the API regardless of the host, so same-origin Just
//          Works on any custom domain bound to the install.
function resolveBaseURL(): string {
  const envURL = process.env.NEXT_PUBLIC_SYNAPSE_URL?.replace(/\/$/, "");
  if (typeof window === "undefined") {
    return envURL || "http://localhost:8080";
  }
  if (envURL) {
    try {
      const envParsed = new URL(envURL);
      const here = window.location;
      // Exact origin match — the conventional production install on
      // the operator's main domain.
      if (envParsed.origin === here.origin) return envURL;
      // Same hostname, different port — `npm run dev` and CI
      // (dashboard at :6790, API at :8080 on the same host).
      if (envParsed.hostname === here.hostname) return envURL;
    } catch {
      // Malformed env URL — fall through to current origin.
    }
  }
  return window.location.origin;
}

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
// list responses see only the `prefix` chip. Revoke rotates deployment
// credentials, so every previously issued deploy key for that deployment
// is invalidated together.
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
  // RFC3339 timestamp; when this is in the future, /version_check is
  // serving from the in-memory cache and will refetch from GitHub
  // afterwards. The dashboard surfaces this as a "next check in MM:SS"
  // countdown so anxious operators who just published a release know
  // when the banner will see the new tag.
  cacheExpiresAt?: string;
  fromCache?: boolean;
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

// GET /v1/admin/host_domain. The current public-URL configuration of the
// Synapse host. `mode` summarises which template setup.sh is rendering:
//   tls               → SYNAPSE_DOMAIN set, plain HTTPS
//   plain             → SYNAPSE_PUBLIC_URL is http://<ip>:<port>, no TLS
//   tls_with_wildcard → SYNAPSE_DOMAIN set + SYNAPSE_BASE_DOMAIN for
//                       per-deployment subdomains
// `fallbackUrls` is the list of always-reachable URLs (typically the IP
// form) — surfaced so operators have something to land on if a domain
// change goes wrong.
export type HostDomainConfig = {
  mode: "tls" | "plain" | "tls_with_wildcard";
  domain?: string;
  baseDomain?: string;
  publicUrl?: string;
  publicIp?: string;
  acmeEmail?: string;
  fallbackUrls?: string[];
};

// GET /v1/admin/host_domain/status/{jobId}. The host-side daemon mirrors
// the upgrade flow — async, polled. `log` is a streaming tail of
// setup.sh's stdout/stderr.
export type HostDomainJobStatus = {
  id: string;
  state: "queued" | "running" | "succeeded" | "failed";
  log?: string[];
  error?: string;
  createdAt?: string;
  startedAt?: string;
  finishedAt?: string;
};

// POST /v1/admin/host_domain. Returns the queued job id + a status URL
// the dashboard polls to surface progress. v1.5.6 adds dnsAuto so the
// dashboard can surface what (if anything) the backend did with a
// stored Cloudflare credential before kicking off the reconfigure.
export type HostDomainChangeResponse = {
  jobId: string;
  statusUrl: string;
  dnsAuto?: HostDomainDNSAutoResult;
};

// HostDomainDNSAutoResult mirrors the Go dnsAutoResult struct. Surfaced
// when the operator ticked "Auto-configure DNS" on the change form.
// `attempted=true && success=true` → green confirmation rendered;
// `attempted=true && success=false` → amber warning with `reason`;
// undefined → checkbox wasn't ticked, render nothing.
export type HostDomainDNSAutoResult = {
  attempted: boolean;
  success: boolean;
  provider?: string;
  credentialId?: string;
  zone?: string;
  recordName?: string;
  ip?: string;
  ipDetectedVia?: string;
  reason?: string;
};

// Body shape for POST /v1/admin/host_domain. All fields optional — the
// backend interprets the combination:
//   { plainHttp: true }                     → switch to plain HTTP
//   { domain }                              → set/change a TLS domain
//   { domain, baseDomain }                  → wildcard subdomains
//   { acmeEmail }                           → bump the Let's Encrypt
//                                             contact without re-issuing
//   { domain, autoConfigureDns: true }      → upsert the A record via a
//                                             stored Cloudflare credential
//                                             before the reconfigure runs.
//                                             Best-effort; falls through
//                                             to manual DNS on failure.
export type HostDomainChangeInput = {
  domain?: string;
  baseDomain?: string;
  plainHttp?: boolean;
  acmeEmail?: string;
  autoConfigureDns?: boolean;
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
//
// `autoConfigured` and `dnsCredentialId` are populated when the row was
// created via POST /domains/{id}/auto_configure — Synapse pushed an A
// record into the operator's Cloudflare zone using a stored credential.
// The dashboard surfaces a Cloudflare chip on auto-configured rows so the
// operator knows the DNS provider is wired up rather than manual.
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
  autoConfigured?: boolean;
  dnsCredentialId?: string;
};

// DNSProvider is what GET /v1/internal/dns_provider returns. Public
// (no auth) — the Add Domain dialog hits it as the operator types so
// we can offer Cloudflare auto-config when applicable. `provider` is
// derived server-side from the apex domain's NS records; "unknown"
// covers the resolution-failed and not-recognised cases alike.
// `nameservers` is shown verbatim so the operator can spot a typo.
export type DNSProviderDetection = {
  provider:
    | "cloudflare"
    | "route53"
    | "google"
    | "namecheap"
    | "godaddy"
    | "unknown";
  nameservers: string[];
};

// DNSCredential mirrors models.DNSCredential on the backend. The token
// itself is never returned — Synapse keeps the secret encrypted at rest
// and only surfaces label + zones for display. `zones` is populated by
// the backend at create time by hitting Cloudflare's /zones endpoint
// with the provided token; an empty list usually means the token's
// scope is too narrow.
export type DNSCredential = {
  id: string;
  provider: "cloudflare";
  label: string;
  // projectId set ⇒ scoped to a single project (v1.6.4+); unset ⇒
  // instance-wide, managed under /v1/admin/dns_credentials.
  projectId?: string;
  zones: { id: string; name: string }[];
  createdBy?: string;
  createdAt: string;
  lastUsedAt?: string;
  lastError?: string;
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

type ResponseWithHeaders<T> = {
  data: T;
  headers: Headers;
};

async function requestWithHeaders<T>(
  path: string,
  opts: RequestOpts = {},
): Promise<ResponseWithHeaders<T>> {
  const { method = "GET", body, auth = true } = opts;
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (auth) {
    const token = getAccessToken();
    if (token) headers["Authorization"] = `Bearer ${token}`;
  }

  let res: Response;
  try {
    res = await fetch(`${resolveBaseURL()}${path}`, {
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
      // Carry the current path + search through to the login form so
      // the operator lands back where they were after re-authenticating.
      // Critical for v1.6.12 custom dashboard domains: visiting
      // `dashboard.<your>.com` 302s to `/embed/<bound>`, which 401s
      // pre-auth, which used to send the operator to `/login` →
      // `/teams` (wrong page, the project picker). With return_to,
      // post-login lands them at /embed/<bound> as intended.
      const here =
        window.location.pathname + window.location.search;
      const safe =
        here && here !== "/" && !here.startsWith("/login")
          ? `?return_to=${encodeURIComponent(here)}`
          : "";
      window.location.href = `/login${safe}`;
    }
    throw new ApiError(401, "unauthorized", "Session expired");
  }

  if (res.status === 204) {
    return { data: undefined as T, headers: res.headers };
  }

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
  return { data: (json as T) ?? (undefined as T), headers: res.headers };
}

async function request<T>(path: string, opts: RequestOpts = {}): Promise<T> {
  const { data } = await requestWithHeaders<T>(path, opts);
  return data;
}

function withQuery(path: string, params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") q.set(key, String(value));
  }
  const sep = path.includes("?") ? "&" : "?";
  const qs = q.toString();
  return qs ? `${path}${sep}${qs}` : path;
}

async function collectPaginated<R, T>(
  path: string,
  extract: (response: R) => T[],
): Promise<T[]> {
  const out: T[] = [];
  let cursor: string | undefined;
  do {
    const { data, headers } = await requestWithHeaders<R>(
      withQuery(path, { cursor, limit: 100 }),
    );
    out.push(...extract(data));
    cursor = headers.get("X-Next-Cursor") ?? undefined;
  } while (cursor);
  return out;
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
      return collectPaginated<Team[] | { teams: Team[] }, Team>(
        "/v1/teams",
        (r) => (Array.isArray(r) ? r : r.teams ?? []),
      );
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
      return collectPaginated<Project[] | { projects: Project[] }, Project>(
        `/v1/teams/${encodeURIComponent(ref)}/list_projects`,
        (r) => (Array.isArray(r) ? r : r.projects ?? []),
      );
    },
    async listDeployments(ref: string): Promise<Deployment[]> {
      // Team-scoped flat list across every project — used by the home view's
      // "Deployments" tab. Mirrors per-project list_deployments shape.
      return collectPaginated<Deployment[] | { deployments: Deployment[] }, Deployment>(
        `/v1/teams/${encodeURIComponent(ref)}/list_deployments`,
        (r) => (Array.isArray(r) ? r : r.deployments ?? []),
      );
    },
    async listMembers(ref: string): Promise<TeamMember[]> {
      return collectPaginated<TeamMember[], TeamMember>(
        `/v1/teams/${encodeURIComponent(ref)}/list_members`,
        (r) => r,
      );
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
      return collectPaginated<Deployment[] | { deployments: Deployment[] }, Deployment>(
        `/v1/projects/${encodeURIComponent(id)}/list_deployments`,
        (r) => (Array.isArray(r) ? r : r.deployments ?? []),
      );
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
    // Project-scoped DNS credentials (v1.6.4+). Mirror of
    // api.admin.dnsCredentials but scoped to a single project — each
    // client of the agency keeps its Cloudflare token alongside its
    // project. Auto-configure flow picks the project's credentials
    // first and falls back to instance-wide as a safety net.
    dnsCredentials: {
      async list(id: string): Promise<DNSCredential[]> {
        const resp = await request<{ credentials: DNSCredential[] }>(
          `/v1/projects/${encodeURIComponent(id)}/dns_credentials`,
        );
        return resp.credentials ?? [];
      },
      addCloudflare(
        id: string,
        token: string,
        label: string,
      ): Promise<DNSCredential> {
        return request<DNSCredential>(
          `/v1/projects/${encodeURIComponent(id)}/dns_credentials/cloudflare`,
          { method: "POST", body: { token, label } },
        );
      },
      delete(projectId: string, credentialId: string): Promise<void> {
        return request<void>(
          `/v1/projects/${encodeURIComponent(projectId)}/dns_credentials/${encodeURIComponent(credentialId)}`,
          { method: "DELETE" },
        );
      },
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
    // Auto-configure DNS for a domain via a stored DNSCredential. The
    // backend pushes an A record into the matching zone (Cloudflare for
    // now) and stamps `autoConfigured=true` on the row. credentialId is
    // optional — when omitted the backend picks the first credential
    // whose zones match the domain's apex.
    autoConfigureDomain(
      name: string,
      domainId: string,
      credentialId?: string,
    ): Promise<DeploymentDomain> {
      return request<DeploymentDomain>(
        `/v1/deployments/${encodeURIComponent(name)}/domains/${encodeURIComponent(domainId)}/auto_configure`,
        { method: "POST", body: credentialId ? { credentialId } : {} },
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

  // Public DNS helpers. No auth — used by the Add Domain dialog to
  // detect the apex domain's nameserver provider as the operator types.
  dns: {
    detectProvider(domain: string): Promise<DNSProviderDetection> {
      return request<DNSProviderDetection>(
        `/v1/internal/dns_provider?domain=${encodeURIComponent(domain)}`,
        { auth: false },
      );
    },
  },

  // Instance-level admin operations: version_check + auto-upgrade.
  // Gated to users.is_instance_admin — see synapse/internal/api/admin.go.
  admin: {
    versionCheck(): Promise<VersionCheck> {
      return request<VersionCheck>("/v1/admin/version_check");
    },
    // Force-bust the GitHub release cache. Synapse normally caches the
    // /releases/latest fetch for 15 minutes (GitHub's unauthenticated
    // limit is 60/hr); this endpoint clears the cache and re-fetches
    // immediately. Backend rate-limits at 30s between busts to keep a
    // misbehaving caller from blowing through the GitHub limit.
    versionCheckRefresh(): Promise<VersionCheck> {
      return request<VersionCheck>("/v1/admin/version_check/refresh", {
        method: "POST",
      });
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
    // Host-domain reconfiguration (setup.sh --change-domain). Same shape
    // as the upgrade flow: queue a job, poll a status endpoint. The
    // actual reconfiguration runs on the host (touches Caddy + .env +
    // compose), not inside the synapse-api container.
    hostDomain: {
      get(): Promise<HostDomainConfig> {
        return request<HostDomainConfig>("/v1/admin/host_domain");
      },
      change(body: HostDomainChangeInput): Promise<HostDomainChangeResponse> {
        return request<HostDomainChangeResponse>("/v1/admin/host_domain", {
          method: "POST",
          body,
        });
      },
      status(jobId: string): Promise<HostDomainJobStatus> {
        return request<HostDomainJobStatus>(
          `/v1/admin/host_domain/status/${encodeURIComponent(jobId)}`,
        );
      },
    },
    // DNS credentials — instance-admin only. Stored credentials let
    // Synapse push records into the operator's DNS zone when adding a
    // custom domain to a deployment, instead of asking the operator to
    // edit DNS by hand. Cloudflare is the only supported provider in
    // v1.4.x; the shape leaves room for route53 / google / etc.
    dnsCredentials: {
      // Backend wraps the array in { credentials: [...] } (matches the
      // listDNSCredentialsResp Go struct in dns_credentials.go). Pre-
      // v1.5.5 the frontend asked for `DNSCredential[]` directly, so
      // SWR ended up holding the wrapper object — `data.length` was
      // undefined, neither the empty-state nor the list ever rendered,
      // and operators saw a blank panel after adding credentials. The
      // Playwright spec mocked `[]` directly, hiding the divergence
      // until real-VPS exposed it. Unwrap explicitly.
      async list(): Promise<DNSCredential[]> {
        const resp = await request<{ credentials: DNSCredential[] }>(
          "/v1/admin/dns_credentials",
        );
        return resp.credentials ?? [];
      },
      addCloudflare(token: string, label: string): Promise<DNSCredential> {
        return request<DNSCredential>("/v1/admin/dns_credentials/cloudflare", {
          method: "POST",
          body: { token, label },
        });
      },
      delete(id: string): Promise<void> {
        return request<void>(
          `/v1/admin/dns_credentials/${encodeURIComponent(id)}`,
          { method: "DELETE" },
        );
      },
    },
  },
};

export { ApiError };
