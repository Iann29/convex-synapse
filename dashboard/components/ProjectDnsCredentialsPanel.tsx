"use client";

import { useMemo, useState } from "react";
import useSWR from "swr";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError, api, type DNSCredential, type ProjectMember } from "@/lib/api";
import { getCurrentUser } from "@/lib/auth";

// Cloudflare API-token deep link — same one the admin panel uses. The
// permission group key pre-fills the "Edit zone DNS" template so the
// operator doesn't have to construct it manually.
const CLOUDFLARE_TOKEN_URL =
  "https://dash.cloudflare.com/profile/api-tokens?permissionGroupKeys=%5B%7B%22key%22%3A%22dns_write%22%7D%5D";

type Props = {
  projectId: string;
};

function relativeTime(iso?: string): string {
  if (!iso) return "—";
  const ts = Date.parse(iso);
  if (Number.isNaN(ts)) return iso;
  const sec = Math.round((Date.now() - ts) / 1000);
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

// ProjectDnsCredentialsPanel — same shape as the instance-admin panel
// but scoped to a single project. Each row added here lives alongside
// the project rather than in the global /admin pool, which matches the
// agency model ("Cloudflare token for client X lives inside client X's
// project"). The auto-configure flow picks rows here before falling
// back to instance-wide credentials.
//
// canManage controls whether the Add / Delete affordances render. The
// backend re-checks (canAdminProject) — this is purely UX.
export function ProjectDnsCredentialsPanel({ projectId }: Props) {
  const [shown, setShown] = useState(false);

  const swrKey = shown ? `/v1/projects/${projectId}/dns_credentials` : null;
  const { data, error, isLoading, mutate } = useSWR<DNSCredential[]>(
    swrKey,
    () => api.projects.dnsCredentials.list(projectId),
    { revalidateOnFocus: false, shouldRetryOnError: false },
  );

  // Derive the caller's effective role from the members listing — same
  // pattern as ProjectMembersPanel. Avoids a second API endpoint for
  // "what's my role" while keeping the source of truth in one place.
  const me = getCurrentUser();
  const { data: members } = useSWR<ProjectMember[]>(
    shown ? ["/project-members", projectId] : null,
    () => api.projects.listMembers(projectId),
  );
  const canManage =
    (members?.find((m) => m.id === me?.id)?.role ?? "viewer") === "admin";

  const [addOpen, setAddOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<DNSCredential | null>(null);
  const credentials = useMemo(() => data ?? [], [data]);

  return (
    <Card data-testid="project-dns-credentials-panel">
      <CardBody>
        <button
          type="button"
          onClick={() => setShown((v) => !v)}
          className="flex w-full items-baseline justify-between text-left"
          aria-expanded={shown}
          data-testid="project-dns-credentials-toggle"
        >
          <span className="text-sm font-semibold text-neutral-200">
            DNS credentials
          </span>
          <span className="text-xs text-neutral-400">
            {shown ? "Hide" : "Show"}
          </span>
        </button>
        {!shown && (
          <p className="mt-1 text-xs text-neutral-500">
            Cloudflare tokens scoped to this project. Used to auto-create
            DNS records when you add a custom domain to a deployment.
          </p>
        )}

        {shown && (
          <div
            className="mt-4 space-y-3"
            data-testid="project-dns-credentials-body"
          >
            <p className="text-xs text-neutral-400">
              Project-scoped tokens win over instance-wide credentials for
              any domain whose apex they cover. Tokens never leave the
              server — they're encrypted at rest and only used to push
              records on your behalf.
            </p>

            {canManage && (
              <div className="flex justify-end">
                <Button
                  size="sm"
                  onClick={() => setAddOpen(true)}
                  data-testid="project-dns-credentials-add"
                >
                  Add Cloudflare credential
                </Button>
              </div>
            )}

            {isLoading && (
              <div className="space-y-2">
                <Skeleton className="h-4 w-1/3" />
                <Skeleton className="h-4 w-1/2" />
              </div>
            )}

            {error && (
              <p
                className="text-xs text-red-400"
                data-testid="project-dns-credentials-load-error"
              >
                {error instanceof ApiError
                  ? error.message
                  : "Could not load DNS credentials"}
              </p>
            )}

            {!isLoading && !error && credentials.length === 0 && (
              <p
                className="rounded-md border border-neutral-800/80 bg-neutral-950 px-3 py-3 text-xs text-neutral-400"
                data-testid="project-dns-credentials-empty"
              >
                No credentials saved for this project yet.{" "}
                {canManage
                  ? "Add a Cloudflare token above to enable DNS auto-configuration on this project's custom domains."
                  : "A project admin can add a Cloudflare token to enable DNS auto-configuration."}
              </p>
            )}

            {credentials.length > 0 && (
              <ul
                className="space-y-2"
                aria-label="Project DNS credentials"
                data-testid="project-dns-credentials-list"
              >
                {credentials.map((c) => (
                  <CredentialRow
                    key={c.id}
                    credential={c}
                    canManage={canManage}
                    onDelete={() => setDeleteTarget(c)}
                  />
                ))}
              </ul>
            )}
          </div>
        )}
      </CardBody>

      <AddCloudflareDialog
        open={addOpen}
        projectId={projectId}
        onClose={() => setAddOpen(false)}
        onAdded={async () => {
          setAddOpen(false);
          await mutate();
        }}
      />

      <DeleteDialog
        target={deleteTarget}
        projectId={projectId}
        onClose={() => setDeleteTarget(null)}
        onDeleted={async () => {
          setDeleteTarget(null);
          await mutate();
        }}
      />
    </Card>
  );
}

function CredentialRow({
  credential,
  canManage,
  onDelete,
}: {
  credential: DNSCredential;
  canManage: boolean;
  onDelete: () => void;
}) {
  const [zonesOpen, setZonesOpen] = useState(false);
  const zoneCount = credential.zones?.length ?? 0;
  return (
    <li
      className="rounded-md border border-neutral-800/80 bg-neutral-950/40 px-3 py-2.5 text-xs"
      data-testid={`project-dns-credential-row-${credential.id}`}
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="truncate font-medium text-neutral-100">
          {credential.label}
        </span>
        <Badge tone="neutral">Cloudflare</Badge>
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
            >
              {zonesOpen ? "Hide zones" : "Show zones"}
            </Button>
          )}
          {canManage && (
            <Button
              variant="ghost"
              size="sm"
              onClick={onDelete}
              aria-label={`Delete credential ${credential.label}`}
              data-testid={`project-dns-credential-delete-${credential.id}`}
            >
              Delete
            </Button>
          )}
        </span>
      </div>
      {credential.lastError && (
        <p className="mt-2 rounded border border-red-900/60 bg-red-950/40 px-2 py-1 text-[11px] text-red-300">
          {credential.lastError}
        </p>
      )}
      {zonesOpen && zoneCount > 0 && (
        <ul className="mt-2 grid grid-cols-1 gap-1 sm:grid-cols-2">
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
  projectId,
  onClose,
  onAdded,
}: {
  open: boolean;
  projectId: string;
  onClose: () => void;
  onAdded: () => Promise<void>;
}) {
  if (!open) return null;
  return (
    <AddDialogInner
      projectId={projectId}
      onClose={onClose}
      onAdded={onAdded}
    />
  );
}

function AddDialogInner({
  projectId,
  onClose,
  onAdded,
}: {
  projectId: string;
  onClose: () => void;
  onAdded: () => Promise<void>;
}) {
  const [label, setLabel] = useState("");
  const [token, setToken] = useState("");
  const [reveal, setReveal] = useState(false);
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
      await api.projects.dnsCredentials.addCloudflare(
        projectId,
        token.trim(),
        label.trim(),
      );
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
        data-testid="project-dns-credentials-add-dialog"
      >
        <div className="space-y-1.5">
          <label
            htmlFor="proj-dns-label"
            className="block text-xs text-neutral-400"
          >
            Label
          </label>
          <Input
            id="proj-dns-label"
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            placeholder="Client A — flert.digital"
            autoComplete="off"
            data-testid="project-dns-credential-label-input"
          />
        </div>
        <div className="space-y-1.5">
          <label
            htmlFor="proj-dns-token"
            className="block text-xs text-neutral-400"
          >
            API token
          </label>
          <div className="relative">
            <Input
              id="proj-dns-token"
              type={reveal ? "text" : "password"}
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder="cf-api-token"
              autoComplete="off"
              spellCheck={false}
              className="pr-20"
              data-testid="project-dns-credential-token-input"
            />
            <button
              type="button"
              onClick={() => setReveal((v) => !v)}
              className="absolute right-2 top-1/2 -translate-y-1/2 rounded px-2 py-1 text-[11px] text-neutral-400 hover:bg-neutral-800 hover:text-neutral-200"
              aria-label={reveal ? "Hide token" : "Show token"}
            >
              {reveal ? "Hide" : "Show"}
            </button>
          </div>
          <p className="text-[11px] text-neutral-500">
            Need a token?{" "}
            <a
              href={CLOUDFLARE_TOKEN_URL}
              target="_blank"
              rel="noopener noreferrer"
              className="text-violet-300 underline-offset-2 hover:underline"
            >
              Create one on Cloudflare
            </a>{" "}
            with <code className="font-mono">Zone:DNS:Edit</code> +{" "}
            <code className="font-mono">Zone:Zone:Read</code> scoped to the
            zone(s) this project's domains live in.
          </p>
        </div>
        {error && (
          <p
            className="rounded border border-red-900/60 bg-red-950/40 px-3 py-2 text-xs text-red-300"
            role="alert"
            data-testid="project-dns-credentials-add-error"
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
          >
            Cancel
          </Button>
          <Button
            type="submit"
            disabled={pending}
            data-testid="project-dns-credentials-add-submit"
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
  projectId,
  onClose,
  onDeleted,
}: {
  target: DNSCredential | null;
  projectId: string;
  onClose: () => void;
  onDeleted: () => Promise<void>;
}) {
  if (!target) return null;
  return (
    <DeleteDialogInner
      target={target}
      projectId={projectId}
      onClose={onClose}
      onDeleted={onDeleted}
    />
  );
}

function DeleteDialogInner({
  target,
  projectId,
  onClose,
  onDeleted,
}: {
  target: DNSCredential;
  projectId: string;
  onClose: () => void;
  onDeleted: () => Promise<void>;
}) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [errorCode, setErrorCode] = useState<string | undefined>();

  const submit = async () => {
    setPending(true);
    setError(null);
    setErrorCode(undefined);
    try {
      await api.projects.dnsCredentials.delete(projectId, target.id);
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

  const inUse = errorCode === "credential_in_use";

  return (
    <Dialog
      open
      onClose={() => !pending && onClose()}
      title={`Delete "${target.label}"?`}
    >
      <div
        className="space-y-3"
        data-testid="project-dns-credentials-delete-dialog"
      >
        <p className="text-xs text-neutral-300">
          Synapse will forget this token. Existing deployment domains that
          were auto-configured with it keep working; only future
          auto-configuration breaks.
        </p>
        {error && (
          <div
            className="space-y-1 rounded border border-red-900/60 bg-red-950/40 px-3 py-2 text-xs text-red-300"
            role="alert"
          >
            {inUse && (
              <p className="font-semibold">
                This credential is in use. Remove the dependent domains
                first, then delete it.
              </p>
            )}
            <p className={inUse ? "text-red-200/80" : ""}>{error}</p>
          </div>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose} disabled={pending}>
            Cancel
          </Button>
          <Button
            variant="danger"
            onClick={submit}
            disabled={pending}
            data-testid="project-dns-credentials-delete-confirm"
          >
            {pending ? "Deleting…" : "Delete"}
          </Button>
        </div>
      </div>
    </Dialog>
  );
}
