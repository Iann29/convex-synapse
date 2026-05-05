"use client";

import { useMemo, useState } from "react";
import useSWR from "swr";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  ApiError,
  api,
  type DeploymentDomain,
} from "@/lib/api";

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

  const domains = useMemo(() => data ?? [], [data]);

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
      await api.deployments.addDomain(deploymentName, trimmed, role);
      setDomain("");
      setRole("api");
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
            {pending ? "Adding…" : "Add"}
          </Button>
        </form>

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
        </div>

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
                  {d.status === "active" && d.dnsVerifiedAt && (
                    <span className="text-neutral-500">
                      verified {relativeTime(d.dnsVerifiedAt)}
                    </span>
                  )}
                  <span className="ml-auto flex shrink-0 gap-2">
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
