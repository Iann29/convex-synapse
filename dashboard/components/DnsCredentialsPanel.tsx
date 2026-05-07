"use client";

import { useMemo, useState } from "react";
import useSWR from "swr";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardBody,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError, api, type DNSCredential } from "@/lib/api";

// Cloudflare API-token deep link. Pre-fills the "Edit zone DNS" template
// on the operator's dashboard so they don't have to wade through every
// permission group. Token still needs Zone:DNS:Edit + Zone:Zone:Read,
// scoped to whichever zone(s) Synapse should touch — surfaced in the
// helper text below.
const CLOUDFLARE_TOKEN_URL =
  "https://dash.cloudflare.com/profile/api-tokens?permissionGroupKeys=%5B%7B%22key%22%3A%22dns_write%22%7D%5D";

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

function providerLabel(p: DNSCredential["provider"]): string {
  if (p === "cloudflare") return "Cloudflare";
  return p;
}

// DnsCredentialsPanel — instance-admin surface that registers DNS
// provider tokens (Cloudflare today). Stored credentials let the per-
// deployment custom-domain flow push DNS records on the operator's
// behalf instead of asking them to copy/paste an A record by hand.
//
// Auth: `/admin/layout.tsx` already enforces is_instance_admin and
// redirects everyone else to /teams. The backend re-checks anyway.
export function DnsCredentialsPanel() {
  const { data, error, isLoading, mutate } = useSWR<DNSCredential[]>(
    "/v1/admin/dns_credentials",
    () => api.admin.dnsCredentials.list(),
    {
      revalidateOnFocus: false,
      shouldRetryOnError: false,
    },
  );

  const [addOpen, setAddOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<DNSCredential | null>(null);

  const credentials = useMemo(() => data ?? [], [data]);

  return (
    <div className="space-y-6" data-testid="dns-credentials-panel">
      <Card>
        <CardHeader>
          <CardTitle>DNS credentials</CardTitle>
          <CardDescription>
            Register DNS provider tokens so Synapse can configure custom
            domains for deployments automatically. Tokens are encrypted at
            rest; we never echo them back.
          </CardDescription>
        </CardHeader>
        <CardBody className="space-y-4">
          <div className="flex justify-end">
            <Button
              onClick={() => setAddOpen(true)}
              data-testid="dns-credentials-add-cloudflare"
            >
              Add Cloudflare credential
            </Button>
          </div>

          {isLoading && (
            <div className="space-y-2">
              <Skeleton className="h-4 w-1/3" />
              <Skeleton className="h-4 w-1/2" />
              <Skeleton className="h-4 w-1/4" />
            </div>
          )}

          {error && (
            <p
              className="text-xs text-red-400"
              data-testid="dns-credentials-load-error"
            >
              {error instanceof ApiError
                ? error.message
                : "Could not load DNS credentials"}
            </p>
          )}

          {!isLoading && !error && credentials.length === 0 && (
            <p
              className="rounded-md border border-neutral-800/80 bg-neutral-950 px-3 py-3 text-xs text-neutral-400"
              data-testid="dns-credentials-empty"
            >
              No credentials yet. Add a Cloudflare token above so you can
              auto-configure DNS for custom domains.
            </p>
          )}

          {credentials.length > 0 && (
            <ul
              className="space-y-2"
              aria-label="DNS credentials"
              data-testid="dns-credentials-list"
            >
              {credentials.map((c) => (
                <CredentialRow
                  key={c.id}
                  credential={c}
                  onDelete={() => setDeleteTarget(c)}
                />
              ))}
            </ul>
          )}
        </CardBody>
      </Card>

      <AddCloudflareDialog
        open={addOpen}
        onClose={() => setAddOpen(false)}
        onAdded={async () => {
          setAddOpen(false);
          await mutate();
        }}
      />

      <DeleteDialog
        target={deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onDeleted={async () => {
          setDeleteTarget(null);
          await mutate();
        }}
      />
    </div>
  );
}

function CredentialRow({
  credential,
  onDelete,
}: {
  credential: DNSCredential;
  onDelete: () => void;
}) {
  const [zonesOpen, setZonesOpen] = useState(false);
  const zoneCount = credential.zones?.length ?? 0;

  return (
    <li
      className="rounded-md border border-neutral-800/80 bg-neutral-950/40 px-3 py-2.5 text-xs"
      data-testid={`dns-credential-row-${credential.id}`}
    >
      <div className="flex flex-wrap items-center gap-2">
        <span
          className="truncate font-medium text-neutral-100"
          data-testid={`dns-credential-label-${credential.id}`}
        >
          {credential.label}
        </span>
        <Badge tone="neutral">{providerLabel(credential.provider)}</Badge>
        <Badge tone={zoneCount > 0 ? "green" : "yellow"}>
          {zoneCount} {zoneCount === 1 ? "zone" : "zones"}
        </Badge>
        <span className="text-neutral-500">
          last used {relativeTime(credential.lastUsedAt)}
        </span>
        <span className="ml-auto flex shrink-0 gap-2">
          {zoneCount > 0 && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setZonesOpen((v) => !v)}
              aria-expanded={zonesOpen}
              aria-label={
                zonesOpen
                  ? `Hide zones for ${credential.label}`
                  : `Show zones for ${credential.label}`
              }
              data-testid={`dns-credential-toggle-zones-${credential.id}`}
            >
              {zonesOpen ? "Hide zones" : "Show zones"}
            </Button>
          )}
          <Button
            variant="ghost"
            size="sm"
            onClick={onDelete}
            aria-label={`Delete credential ${credential.label}`}
            data-testid={`dns-credential-delete-${credential.id}`}
          >
            Delete
          </Button>
        </span>
      </div>

      {credential.lastError && (
        <p
          className="mt-2 rounded border border-red-900/60 bg-red-950/40 px-2 py-1 text-[11px] text-red-300"
          data-testid={`dns-credential-error-${credential.id}`}
        >
          {credential.lastError}
        </p>
      )}

      {zonesOpen && zoneCount > 0 && (
        <ul
          className="mt-2 grid grid-cols-1 gap-1 sm:grid-cols-2"
          data-testid={`dns-credential-zones-${credential.id}`}
        >
          {credential.zones.map((z) => (
            <li
              key={z.id}
              className="truncate rounded bg-neutral-900 px-2 py-1 font-mono text-[11px] text-neutral-300"
            >
              {z.name}
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

function AddCloudflareDialog({
  open,
  onClose,
  onAdded,
}: {
  open: boolean;
  onClose: () => void;
  onAdded: () => Promise<void>;
}) {
  if (!open) return null;
  return <AddCloudflareDialogInner onClose={onClose} onAdded={onAdded} />;
}

function AddCloudflareDialogInner({
  onClose,
  onAdded,
}: {
  onClose: () => void;
  onAdded: () => Promise<void>;
}) {
  const [label, setLabel] = useState("");
  const [token, setToken] = useState("");
  const [revealToken, setRevealToken] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    if (!label.trim()) {
      setError("Label is required");
      return;
    }
    if (!token.trim()) {
      setError("API token is required");
      return;
    }
    setPending(true);
    try {
      await api.admin.dnsCredentials.addCloudflare(token.trim(), label.trim());
      await onAdded();
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : "Could not add credential",
      );
    } finally {
      setPending(false);
    }
  };

  return (
    <Dialog
      open
      onClose={() => !pending && onClose()}
      title="Add Cloudflare credential"
    >
      <form
        onSubmit={submit}
        className="space-y-4"
        aria-label="Add Cloudflare credential"
        data-testid="dns-credentials-add-dialog"
      >
        <div className="space-y-1.5">
          <label
            htmlFor="dns-credential-label"
            className="block text-xs text-neutral-400"
          >
            Label
          </label>
          <Input
            id="dns-credential-label"
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            placeholder="Personal CF account"
            autoComplete="off"
            data-testid="dns-credential-label-input"
          />
          <p className="text-[11px] text-neutral-500">
            A short name so you can tell tokens apart later.
          </p>
        </div>

        <div className="space-y-1.5">
          <label
            htmlFor="dns-credential-token"
            className="block text-xs text-neutral-400"
          >
            API token
          </label>
          <div className="relative">
            <Input
              id="dns-credential-token"
              type={revealToken ? "text" : "password"}
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder="cf-api-token"
              autoComplete="off"
              spellCheck={false}
              className="pr-20"
              data-testid="dns-credential-token-input"
            />
            <button
              type="button"
              onClick={() => setRevealToken((v) => !v)}
              className="absolute right-2 top-1/2 -translate-y-1/2 rounded px-2 py-1 text-[11px] text-neutral-400 hover:bg-neutral-800 hover:text-neutral-200"
              aria-label={revealToken ? "Hide token" : "Show token"}
              data-testid="dns-credential-token-toggle"
            >
              {revealToken ? "Hide" : "Show"}
            </button>
          </div>
          <p className="text-[11px] text-neutral-500">
            Need a token?{" "}
            <a
              href={CLOUDFLARE_TOKEN_URL}
              target="_blank"
              rel="noopener noreferrer"
              className="text-violet-300 underline-offset-2 hover:underline"
              data-testid="dns-credential-cloudflare-link"
            >
              Open Cloudflare and create one
            </a>{" "}
            — needs <code className="font-mono">Zone:DNS:Edit</code> +{" "}
            <code className="font-mono">Zone:Zone:Read</code> scoped to the
            zone(s) you want Synapse to manage.
          </p>
        </div>

        {error && (
          <p
            className="rounded border border-red-900/60 bg-red-950/40 px-3 py-2 text-xs text-red-300"
            role="alert"
            data-testid="dns-credentials-add-error"
          >
            {error}
          </p>
        )}

        <div className="flex justify-end gap-2">
          <Button
            type="button"
            variant="ghost"
            onClick={onClose}
            disabled={pending}
            data-testid="dns-credentials-add-cancel"
          >
            Cancel
          </Button>
          <Button
            type="submit"
            disabled={pending}
            data-testid="dns-credentials-add-submit"
          >
            {pending ? "Adding…" : "Add credential"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function DeleteDialog({
  target,
  onClose,
  onDeleted,
}: {
  target: DNSCredential | null;
  onClose: () => void;
  onDeleted: () => Promise<void>;
}) {
  if (!target) return null;
  return (
    <DeleteDialogInner
      target={target}
      onClose={onClose}
      onDeleted={onDeleted}
    />
  );
}

function DeleteDialogInner({
  target,
  onClose,
  onDeleted,
}: {
  target: DNSCredential;
  onClose: () => void;
  onDeleted: () => Promise<void>;
}) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [errorCode, setErrorCode] = useState<string | undefined>(undefined);

  const submit = async () => {
    setPending(true);
    setError(null);
    setErrorCode(undefined);
    try {
      await api.admin.dnsCredentials.delete(target.id);
      await onDeleted();
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
        setErrorCode(err.code);
      } else {
        setError("Could not delete credential");
      }
    } finally {
      setPending(false);
    }
  };

  // 409 credential_in_use is the load-bearing error path: we surface a
  // clearer "remove the dependent domains first" hint instead of the
  // raw backend message, but keep the original message visible too.
  const inUse = errorCode === "credential_in_use";

  return (
    <Dialog
      open
      onClose={() => !pending && onClose()}
      title={`Delete "${target.label}"?`}
    >
      <div className="space-y-3" data-testid="dns-credentials-delete-dialog">
        <p className="text-xs text-neutral-300">
          Synapse will forget this token. Any deployment domains that were
          auto-configured with it will keep working — only future
          auto-configuration breaks.
        </p>

        {error && (
          <div
            className="space-y-1 rounded border border-red-900/60 bg-red-950/40 px-3 py-2 text-xs text-red-300"
            role="alert"
            data-testid="dns-credentials-delete-error"
          >
            {inUse && (
              <p className="font-semibold">
                This credential is in use. Remove the dependent domains
                first, then delete the credential.
              </p>
            )}
            <p className={inUse ? "text-red-200/80" : ""}>{error}</p>
          </div>
        )}

        <div className="flex justify-end gap-2">
          <Button
            variant="ghost"
            onClick={onClose}
            disabled={pending}
            data-testid="dns-credentials-delete-cancel"
          >
            Cancel
          </Button>
          <Button
            variant="danger"
            onClick={submit}
            disabled={pending}
            data-testid="dns-credentials-delete-confirm"
          >
            {pending ? "Deleting…" : "Delete"}
          </Button>
        </div>
      </div>
    </Dialog>
  );
}
