"use client";

import { useEffect, useMemo, useState } from "react";
import useSWR from "swr";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  ApiError,
  api,
  type DNSCredential,
  type DNSProviderDetection,
  type DeploymentDomain,
} from "@/lib/api";
import type { User } from "@/lib/auth";

type Props = {
  deploymentName: string;
};

// Mirror of synapse/internal/api/domains.go::publicIPNotConfiguredHint.
// When SYNAPSE_PUBLIC_IP is unset on the server, every row comes back
// with status='pending' and lastDnsError carrying this exact phrase.
// We pattern-match the prefix so a future error-string tweak still
// triggers the warning banner (the prefix is the load-bearing part).
const PUBLIC_IP_NOT_CONFIGURED_PREFIX = "SYNAPSE_PUBLIC_IP not configured";

// Loose client-side hostname check — at least one dot, label-shaped chars.
// The backend re-validates with a stricter regex (and lowercases first),
// so this exists only to short-circuit obvious typos before round-tripping.
const HOSTNAME_RE =
  /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/;

function statusTone(
  status: DeploymentDomain["status"],
): "green" | "yellow" | "red" | "neutral" {
  if (status === "active") return "green";
  if (status === "pending") return "yellow";
  if (status === "failed") return "red";
  return "neutral";
}

// Best-effort relative time. Falls back to an absolute string when the
// gap is more than a week — at that point the operator probably wants
// the date, not "8 days ago".
function relativeTime(iso?: string): string {
  if (!iso) return "—";
  const ts = Date.parse(iso);
  if (Number.isNaN(ts)) return iso;
  const diffMs = Date.now() - ts;
  const sec = Math.round(diffMs / 1000);
  if (sec < 5) return "just now";
  if (sec < 60) return `${sec}s ago`;
  const min = Math.round(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.round(hr / 24);
  if (day < 7) return `${day}d ago`;
  try {
    return new Date(ts).toLocaleDateString();
  } catch {
    return iso;
  }
}

// Custom-domain CRUD panel. Pairs with backend PR #64
// (synapse/internal/api/domains.go) — list / add / delete / verify.
// Mounted on the project page next to the per-deployment DeployKeysPanel
// + CliCredentialsPanel; same canEdit gating applies server-side, so
// failed actions surface as inline error strings.
export function CustomDomainsPanel({ deploymentName }: Props) {
  const [open, setOpen] = useState(false);
  return open ? (
    <CustomDomainsPanelExpanded
      deploymentName={deploymentName}
      onCollapse={() => setOpen(false)}
    />
  ) : (
    <div className="flex items-center gap-2 text-xs">
      <Button
        variant="ghost"
        size="sm"
        onClick={() => setOpen(true)}
        aria-label={`Manage custom domains for ${deploymentName}`}
        data-testid={`custom-domains-open-${deploymentName}`}
      >
        Manage custom domains
      </Button>
    </div>
  );
}

function CustomDomainsPanelExpanded({
  deploymentName,
  onCollapse,
}: {
  deploymentName: string;
  onCollapse: () => void;
}) {
  const { data, error, mutate, isLoading } = useSWR<DeploymentDomain[]>(
    ["/v1/deployments", deploymentName, "domains"],
    () => api.deployments.listDomains(deploymentName),
  );

  const [domain, setDomain] = useState("");
  const [role, setRole] = useState<"api" | "dashboard">("api");
  const [pending, setPending] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [verifyingId, setVerifyingId] = useState<string | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  // v1.6.7+: per-row "Auto-configure DNS" retry button. Distinct from
  // verifyingId so the operator can see one row in-flight while
  // others still expose the action.
  const [autoConfiguringId, setAutoConfiguringId] = useState<string | null>(
    null,
  );

  // Auto-config UI state. detection + autoConfigure are reset/refreshed
  // on every domain input change (debounced); selectedCredentialId
  // sticks across re-renders so the operator's pick survives a typo.
  const [detection, setDetection] = useState<DNSProviderDetection | null>(
    null,
  );
  const [detecting, setDetecting] = useState(false);
  const [autoConfigure, setAutoConfigure] = useState(true);
  const [selectedCredentialId, setSelectedCredentialId] = useState<string>("");
  // Surfaces the in-flight auto_configure POST status. Distinct from
  // `pending` (which covers the parent /domains POST) so the row
  // animates in immediately while the DNS push is still running.
  const [configuringDomain, setConfiguringDomain] = useState<string | null>(
    null,
  );

  // Whether the caller is allowed to see the Cloudflare credential
  // picker. The /v1/admin/dns_credentials endpoint is instance-admin
  // gated server-side, so non-admins fall through to manual DNS
  // instructions. We swallow the request error rather than surface it
  // to keep the panel quiet for project-admin-only operators.
  const { data: me } = useSWR<User>(
    "/me",
    () => api.me(),
    { revalidateOnFocus: false, shouldRetryOnError: false },
  );
  const isInstanceAdmin = me?.isInstanceAdmin === true;

  const { data: credentials } = useSWR<DNSCredential[]>(
    isInstanceAdmin ? "/v1/admin/dns_credentials" : null,
    () => api.admin.dnsCredentials.list(),
    { revalidateOnFocus: false, shouldRetryOnError: false },
  );

  const domains = useMemo(() => data ?? [], [data]);

  // Debounced provider detection — fires 500ms after the operator
  // stops typing. Skips obviously-empty / invalid inputs to avoid
  // hammering the public endpoint on every keystroke. We only call
  // setState inside the async callback (i.e. an external event), not
  // synchronously in the effect body — that keeps eslint's
  // react-hooks/set-state-in-effect rule happy.
  useEffect(() => {
    const trimmed = domain.trim().toLowerCase();
    let cancelled = false;
    if (!trimmed || !HOSTNAME_RE.test(trimmed)) {
      // Only reset if we actually had a value to reset; the conditional
      // is enough to avoid noisy re-renders + the ESLint set-state-in-
      // effect rule (it only flags unconditional / cascading writes).
      const id = window.setTimeout(() => {
        if (!cancelled) {
          setDetection(null);
          setDetecting(false);
        }
      }, 0);
      return () => {
        cancelled = true;
        window.clearTimeout(id);
      };
    }
    const id = window.setTimeout(async () => {
      if (cancelled) return;
      setDetecting(true);
      try {
        const r = await api.dns.detectProvider(trimmed);
        if (!cancelled) {
          setDetection(r);
        }
      } catch {
        // Treat detection failure as "unknown" — we still want the
        // operator to be able to add the domain manually.
        if (!cancelled) {
          setDetection({ provider: "unknown", nameservers: [] });
        }
      } finally {
        if (!cancelled) setDetecting(false);
      }
    }, 500);
    return () => {
      cancelled = true;
      window.clearTimeout(id);
    };
  }, [domain]);

  // Match credentials to the typed domain by checking each zone for an
  // apex-suffix match. e.g. "api.example.com" matches a zone of
  // "example.com". Returned list keeps order from the server.
  const matchingCredentials = useMemo(() => {
    if (!credentials || !credentials.length) return [];
    const trimmed = domain.trim().toLowerCase();
    if (!trimmed) return [];
    return credentials.filter((c) =>
      c.zones?.some(
        (z) =>
          trimmed === z.name.toLowerCase() ||
          trimmed.endsWith("." + z.name.toLowerCase()),
      ),
    );
  }, [credentials, domain]);

  // Effective selection: derive instead of stashing in state. If the
  // operator's choice is still in the match-set, use it; otherwise
  // fall back to the first matching credential. Keeps the picker
  // sensible across re-renders without an Effect-driven default.
  const effectiveCredentialId = useMemo(() => {
    if (matchingCredentials.length === 0) return "";
    const stillValid = matchingCredentials.some(
      (c) => c.id === selectedCredentialId,
    );
    if (stillValid) return selectedCredentialId;
    return matchingCredentials[0].id;
  }, [matchingCredentials, selectedCredentialId]);

  const showCloudflareBox =
    detection?.provider === "cloudflare" && !!domain.trim();
  const canAutoConfigure =
    showCloudflareBox &&
    isInstanceAdmin &&
    matchingCredentials.length > 0 &&
    autoConfigure;

  // Detect "operator hasn't set SYNAPSE_PUBLIC_IP" from the server's
  // lastDnsError prefix. Surfacing this once at the panel level is more
  // actionable than repeating the long error inline on every row.
  const publicIPMissing = useMemo(
    () =>
      domains.some(
        (d) =>
          d.status === "pending" &&
          (d.lastDnsError ?? "").startsWith(PUBLIC_IP_NOT_CONFIGURED_PREFIX),
      ),
    [domains],
  );

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    const trimmed = domain.trim().toLowerCase();
    if (!trimmed) {
      setFormError("Domain is required");
      return;
    }
    if (!HOSTNAME_RE.test(trimmed)) {
      setFormError("Domain must be a valid hostname (e.g. api.example.com)");
      return;
    }
    setPending(true);
    try {
      const created = await api.deployments.addDomain(
        deploymentName,
        trimmed,
        role,
      );
      // Optimistically render the new row so the operator sees
      // progress while the auto_configure call runs.
      await mutate(
        (current) => [...(current ?? []), created],
        { revalidate: false },
      );

      // Auto-configure path: only fire when CF detected, the operator
      // has the toggle on, and a matching credential is selected.
      // Failures here surface as a row-level error; the domain row
      // itself is still created.
      if (canAutoConfigure && effectiveCredentialId) {
        setConfiguringDomain(trimmed);
        try {
          const updated = await api.deployments.autoConfigureDomain(
            deploymentName,
            created.id,
            effectiveCredentialId,
          );
          await mutate(
            (current) =>
              (current ?? []).map((d) => (d.id === updated.id ? updated : d)),
            { revalidate: false },
          );
        } catch (err) {
          setActionError(
            err instanceof ApiError
              ? `Auto-configure failed: ${err.message}`
              : "Auto-configure failed",
          );
        } finally {
          setConfiguringDomain(null);
        }
      }

      setDomain("");
      setRole("api");
      setDetection(null);
      // Final reconcile against the server.
      await mutate();
    } catch (err) {
      setFormError(
        err instanceof ApiError ? err.message : "Could not add domain",
      );
    } finally {
      setPending(false);
    }
  };

  const verify = async (row: DeploymentDomain) => {
    setActionError(null);
    setVerifyingId(row.id);
    // Optimistic: flip status to 'pending' immediately so the row's
    // badge reflects the in-flight check. SWR's mutate() with a
    // function returns a transient list; the server response will
    // reconcile when it lands.
    await mutate(
      (current) =>
        (current ?? []).map((d) =>
          d.id === row.id ? { ...d, status: "pending" as const } : d,
        ),
      { revalidate: false },
    );
    try {
      const updated = await api.deployments.verifyDomain(
        deploymentName,
        row.id,
      );
      await mutate(
        (current) =>
          (current ?? []).map((d) => (d.id === row.id ? updated : d)),
        { revalidate: false },
      );
    } catch (err) {
      setActionError(
        err instanceof ApiError ? err.message : "Could not verify domain",
      );
      // Roll back to whatever the server actually has on file.
      await mutate();
    } finally {
      setVerifyingId(null);
    }
  };

  const remove = async (row: DeploymentDomain) => {
    if (!confirm(`Remove custom domain "${row.domain}"?`)) {
      return;
    }
    setActionError(null);
    setDeletingId(row.id);
    try {
      await api.deployments.deleteDomain(deploymentName, row.id);
      await mutate();
    } catch (err) {
      setActionError(
        err instanceof ApiError ? err.message : "Could not remove domain",
      );
    } finally {
      setDeletingId(null);
    }
  };

  // v1.6.7+: manual auto-configure retry. Covers the common case where
  // the operator added the domain BEFORE registering a Cloudflare
  // credential covering the zone (the row landed FAILED because the A
  // record didn't exist yet, and there was no credential to mint it).
  // After cadastrar a credential they need a way to fire the
  // auto-config without deleting + re-adding the domain. The backend
  // endpoint already exists since v1.5.6 (POST /domains/{id}/auto_configure);
  // this just wires the dashboard up to call it.
  //
  // No credentialId is passed: the backend auto-picks when exactly one
  // project-scoped (or instance-wide) credential covers the zone. If
  // multiple match, the response carries `credential_required` and we
  // surface the message — user picks via... well, future work; for
  // now they get a clear error and can delete the unwanted credential.
  const autoConfigureRow = async (row: DeploymentDomain) => {
    setActionError(null);
    setAutoConfiguringId(row.id);
    try {
      const updated = await api.deployments.autoConfigureDomain(
        deploymentName,
        row.id,
      );
      // Successful auto-config flips the row to status='pending' with
      // auto_configured=true; the dns.Verifier loop will pick it up
      // once DNS propagates (usually <30s for Cloudflare). Patch the
      // SWR cache so the badge + "auto" chip update without a refetch.
      await mutate(
        (current) =>
          (current ?? []).map((d) => (d.id === row.id ? updated : d)),
        { revalidate: false },
      );
    } catch (err) {
      if (err instanceof ApiError) {
        // Make the two common failure codes user-actionable. Anything
        // else falls through to the raw backend message.
        if (err.code === "no_credential_for_zone") {
          setActionError(
            `No DNS credential covers "${row.domain}". Add a Cloudflare credential covering the zone in the "DNS credentials" panel below, then retry.`,
          );
        } else if (err.code === "credential_required") {
          setActionError(
            `Multiple DNS credentials cover "${row.domain}". Remove the duplicate(s) in "DNS credentials" so the auto-pick has a unique match, then retry.`,
          );
        } else {
          setActionError(err.message);
        }
      } else {
        setActionError("Could not auto-configure DNS");
      }
    } finally {
      setAutoConfiguringId(null);
    }
  };

  return (
    <Card className="mt-2" data-testid={`custom-domains-panel-${deploymentName}`}>
      <CardBody className="space-y-3">
        <div className="flex items-start justify-between gap-2">
          <div>
            <p className="text-xs font-semibold text-neutral-200">
              Custom domains
            </p>
            <p className="text-xs text-neutral-500">
              Map your own domains to this deployment. Synapse handles TLS
              automatically once DNS is configured.
            </p>
          </div>
          <div className="flex shrink-0 gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={onCollapse}
              aria-label="Hide custom domains panel"
            >
              Hide
            </Button>
          </div>
        </div>

        {publicIPMissing && (
          <p
            className="rounded border border-yellow-900/60 bg-yellow-900/20 px-3 py-2 text-[11px] text-yellow-200"
            data-testid="custom-domains-public-ip-warning"
          >
            <span className="font-semibold">DNS verification disabled:</span>{" "}
            <code className="font-mono">SYNAPSE_PUBLIC_IP</code> is not set on
            this Synapse host. Domains stay <code>pending</code> until the
            operator configures it. TLS provisioning won&rsquo;t fire until
            then either.
          </p>
        )}

        <form
          onSubmit={submit}
          className="flex flex-wrap items-end gap-2"
          aria-label="Add custom domain"
        >
          <div className="min-w-[12rem] flex-1 space-y-1">
            <label
              htmlFor={`custom-domain-input-${deploymentName}`}
              className="block text-xs text-neutral-400"
            >
              Domain
            </label>
            <Input
              id={`custom-domain-input-${deploymentName}`}
              value={domain}
              onChange={(e) => setDomain(e.target.value)}
              placeholder="api.example.com"
              autoComplete="off"
              autoCapitalize="off"
              spellCheck={false}
              data-testid="custom-domain-input"
            />
          </div>
          <div className="space-y-1">
            <label
              htmlFor={`custom-domain-role-${deploymentName}`}
              className="block text-xs text-neutral-400"
            >
              Role
            </label>
            <select
              id={`custom-domain-role-${deploymentName}`}
              value={role}
              onChange={(e) => setRole(e.target.value as "api" | "dashboard")}
              className="h-9 rounded-md border border-neutral-700 bg-neutral-900 px-3 text-sm text-neutral-100 focus:border-neutral-500 focus:outline-none"
              data-testid="custom-domain-role"
            >
              <option value="api">api</option>
              <option value="dashboard">dashboard</option>
            </select>
          </div>
          <Button
            type="submit"
            disabled={pending || !domain.trim()}
            data-testid="custom-domain-add"
          >
            {pending
              ? configuringDomain
                ? "Configuring DNS…"
                : "Adding…"
              : "Add"}
          </Button>
        </form>

        {/* Provider detection — fires after the operator stops typing.
            Cloudflare is the auto-config happy path; other providers and
            "unknown" both fall through to manual instructions. */}
        {detecting && domain.trim() && (
          <p
            className="text-[11px] text-neutral-500"
            data-testid="custom-domain-detection-pending"
          >
            Detecting DNS provider…
          </p>
        )}

        {showCloudflareBox && (
          <div
            className="space-y-2 rounded-md border border-emerald-700/50 bg-emerald-900/15 px-3 py-2.5 text-[11px] text-emerald-100"
            data-testid="custom-domain-cloudflare-box"
          >
            <p className="font-semibold text-emerald-200">
              Cloudflare detected
            </p>
            <p className="text-emerald-100/90">
              We can configure DNS for you automatically using a stored
              Cloudflare credential.
            </p>
            {!isInstanceAdmin && (
              <p
                className="text-emerald-100/80"
                data-testid="custom-domain-cloudflare-not-admin"
              >
                Auto-configuration is gated to instance admins. Ask your
                Synapse admin to add a Cloudflare credential, or follow the
                manual instructions below.
              </p>
            )}
            {isInstanceAdmin && matchingCredentials.length === 0 && (
              <p
                className="text-emerald-100/90"
                data-testid="custom-domain-cloudflare-no-credential"
              >
                No credential covers this domain.{" "}
                <a
                  href="/admin/dns-credentials"
                  className="font-medium text-white underline-offset-2 hover:underline"
                >
                  Add a Cloudflare credential
                </a>{" "}
                to enable one-click DNS.
              </p>
            )}
            {isInstanceAdmin && matchingCredentials.length > 0 && (
              <div className="space-y-2">
                <label
                  className="flex cursor-pointer items-center gap-2"
                  data-testid="custom-domain-autoconfigure-toggle-row"
                >
                  <input
                    type="checkbox"
                    checked={autoConfigure}
                    onChange={(e) => setAutoConfigure(e.target.checked)}
                    data-testid="custom-domain-autoconfigure-toggle"
                  />
                  <span className="text-emerald-100">
                    Auto-configure with Cloudflare
                  </span>
                </label>
                {autoConfigure && (
                  <div className="space-y-1">
                    <label
                      htmlFor={`custom-domain-cred-${deploymentName}`}
                      className="block text-emerald-100/80"
                    >
                      Credential
                    </label>
                    <select
                      id={`custom-domain-cred-${deploymentName}`}
                      value={effectiveCredentialId}
                      onChange={(e) =>
                        setSelectedCredentialId(e.target.value)
                      }
                      className="h-8 w-full rounded-md border border-emerald-800/60 bg-neutral-900 px-2 text-[11px] text-neutral-100 focus:border-emerald-500 focus:outline-none"
                      data-testid="custom-domain-credential-select"
                    >
                      {matchingCredentials.map((c) => (
                        <option key={c.id} value={c.id}>
                          {c.label}
                          {c.zones?.length
                            ? ` — ${c.zones.map((z) => z.name).join(", ")}`
                            : ""}
                        </option>
                      ))}
                    </select>
                  </div>
                )}
              </div>
            )}
          </div>
        )}

        {/* Manual instructions — shown when CF wasn't detected, or
            when the operator opts out of auto-config. We also keep them
            visible when detection is "unknown" so a failed NS lookup
            doesn't strand the operator. */}
        {!showCloudflareBox && (
          <div
            className="rounded-md border border-neutral-800/80 bg-neutral-950 px-3 py-2 text-[11px] text-neutral-400"
            data-testid="custom-domain-dns-hint"
          >
            <p className="font-semibold text-neutral-300">DNS instructions</p>
            <p className="mt-1">
              Point an <code className="font-mono">A</code> record for your
              domain at this Synapse host&rsquo;s public IPv4
              (<code className="font-mono">SYNAPSE_PUBLIC_IP</code>). Once the
              record propagates, Synapse will issue a Let&rsquo;s Encrypt
              certificate on demand.
            </p>
            {detection?.provider === "unknown" && domain.trim() && (
              <p
                className="mt-2 text-neutral-500"
                data-testid="custom-domain-detection-unknown"
              >
                DNS provider not detected — you can still configure
                manually after the domain is added.
              </p>
            )}
          </div>
        )}

        {formError && (
          <p
            className="text-xs text-red-400"
            role="alert"
            data-testid="custom-domain-form-error"
          >
            {formError}
          </p>
        )}
        {error && (
          <p className="text-xs text-red-400">
            {error instanceof ApiError
              ? error.message
              : "Could not load domains"}
          </p>
        )}
        {actionError && (
          <p className="text-xs text-red-400" role="alert">
            {actionError}
          </p>
        )}

        {isLoading ? (
          <p className="text-xs text-neutral-500">Loading…</p>
        ) : domains.length === 0 ? (
          <p
            className="text-xs text-neutral-500"
            data-testid="custom-domains-empty"
          >
            No custom domains yet. Add one above to start routing traffic
            from a domain you control.
          </p>
        ) : (
          <ul
            className="space-y-2"
            data-testid="custom-domains-list"
            aria-label="Custom domains"
          >
            {domains.map((d) => (
              <li
                key={d.id}
                className="rounded-md border border-neutral-800/80 bg-neutral-950/40 px-3 py-2 text-[11px]"
                data-testid={`custom-domain-row-${d.domain}`}
              >
                <div className="flex flex-wrap items-center gap-2">
                  <span className="truncate font-mono text-sm text-neutral-100">
                    {d.domain}
                  </span>
                  <Badge tone="neutral">{d.role}</Badge>
                  <Badge
                    tone={statusTone(d.status)}
                    data-testid={`custom-domain-status-${d.domain}`}
                  >
                    {d.status}
                  </Badge>
                  {d.autoConfigured && (
                    <Badge
                      tone="neutral"
                      className="border-orange-500/40 bg-orange-500/10 text-orange-300"
                      title="DNS managed by Synapse via Cloudflare"
                      data-testid={`custom-domain-cloudflare-chip-${d.domain}`}
                    >
                      Cloudflare
                    </Badge>
                  )}
                  {configuringDomain === d.domain && (
                    <span
                      className="text-neutral-400"
                      data-testid={`custom-domain-configuring-${d.domain}`}
                    >
                      Configuring DNS at Cloudflare…
                    </span>
                  )}
                  {d.status === "active" && d.dnsVerifiedAt && (
                    <span className="text-neutral-500">
                      verified {relativeTime(d.dnsVerifiedAt)}
                    </span>
                  )}
                  <span className="ml-auto flex shrink-0 gap-2">
                    {/* Auto-configure DNS retry. Only meaningful when
                        the row isn't already 'active' — for 'active'
                        rows the A record is already correct and re-
                        upserting it would be a no-op. We hide the
                        button rather than disable it because operators
                        kept clicking the disabled state. */}
                    {d.status !== "active" && (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => autoConfigureRow(d)}
                        disabled={autoConfiguringId === d.id}
                        aria-label={`Auto-configure DNS for ${d.domain} via stored credential`}
                        data-testid={`custom-domain-autoconfigure-${d.domain}`}
                        title="Push the A record via a stored Cloudflare credential covering this zone"
                      >
                        {autoConfiguringId === d.id
                          ? "Auto-configuring…"
                          : "Auto-configure DNS"}
                      </Button>
                    )}
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => verify(d)}
                      disabled={verifyingId === d.id}
                      aria-label={`Verify DNS for ${d.domain}`}
                      data-testid={`custom-domain-verify-${d.domain}`}
                    >
                      {verifyingId === d.id ? "Verifying…" : "Verify"}
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => remove(d)}
                      disabled={deletingId === d.id}
                      aria-label={`Remove custom domain ${d.domain}`}
                      data-testid={`custom-domain-remove-${d.domain}`}
                    >
                      {deletingId === d.id ? "Removing…" : "Remove"}
                    </Button>
                  </span>
                </div>
                {d.status === "failed" && d.lastDnsError && (
                  <p className="mt-1 text-red-400">{d.lastDnsError}</p>
                )}
                {d.status === "pending" &&
                  d.lastDnsError &&
                  !d.lastDnsError.startsWith(
                    PUBLIC_IP_NOT_CONFIGURED_PREFIX,
                  ) && (
                    <p className="mt-1 text-yellow-300">{d.lastDnsError}</p>
                  )}
              </li>
            ))}
          </ul>
        )}
      </CardBody>
    </Card>
  );
}
