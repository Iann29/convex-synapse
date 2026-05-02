"use client";

import { useEffect, useState } from "react";
import useSWR from "swr";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  ApiError,
  api,
  type CreateDeployKeyResponse,
  type DeployKey,
} from "@/lib/api";
import { copyToClipboard } from "@/lib/clipboard";

type Props = {
  deploymentName: string;
};

type Format = "env" | "shell";

// DeployKeysPanel mirrors Convex Cloud's "Personal Deployment Settings →
// Deploy Keys" surface: per-deployment named admin keys for CI use.
// Hidden behind a "Manage deploy keys" toggle so the dashboard only
// loads the list when the operator explicitly opens it (the values
// themselves are only shown once at creation time, GitHub-PAT-style).
export function DeployKeysPanel({ deploymentName }: Props) {
  const [open, setOpen] = useState(false);
  return open ? (
    <DeployKeysPanelExpanded
      deploymentName={deploymentName}
      onCollapse={() => setOpen(false)}
    />
  ) : (
    <div className="flex items-center gap-2 text-xs">
      <Button
        variant="ghost"
        size="sm"
        onClick={() => setOpen(true)}
        aria-label={`Manage deploy keys for ${deploymentName}`}
      >
        Manage deploy keys
      </Button>
    </div>
  );
}

function DeployKeysPanelExpanded({
  deploymentName,
  onCollapse,
}: {
  deploymentName: string;
  onCollapse: () => void;
}) {
  const { data, error, mutate, isLoading } = useSWR<{
    deployKeys: DeployKey[];
  }>(["/v1/deployments", deploymentName, "deploy_keys"], () =>
    api.deployments.listDeployKeys(deploymentName),
  );

  const [createOpen, setCreateOpen] = useState(false);
  const [revokingId, setRevokingId] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const onCreated = () => {
    void mutate();
  };

  const revoke = async (key: DeployKey) => {
    if (
      !confirm(
        `Revoke deploy key "${key.name}"?\n\nNote: this hides the key from the dashboard but does NOT immediately invalidate it on the Convex backend. To actually disable the key, rotate the deployment.`,
      )
    ) {
      return;
    }
    setRevokingId(key.id);
    setActionError(null);
    try {
      await api.deployments.revokeDeployKey(deploymentName, key.id);
      await mutate();
    } catch (err) {
      setActionError(
        err instanceof ApiError ? err.message : "Could not revoke deploy key",
      );
    } finally {
      setRevokingId(null);
    }
  };

  const keys = data?.deployKeys ?? [];

  return (
    <Card className="mt-2">
      <CardBody className="space-y-3">
        <div className="flex items-start justify-between gap-2">
          <div>
            <p className="text-xs font-semibold text-neutral-200">
              Deploy keys
            </p>
            <p className="text-xs text-neutral-500">
              Named admin keys for CI integrations (Vercel, GitHub Actions,
              etc). Each key has its own audit trail.
            </p>
          </div>
          <div className="flex shrink-0 gap-2">
            <Button
              size="sm"
              onClick={() => setCreateOpen(true)}
              aria-label="Create a new deploy key"
            >
              + Create
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={onCollapse}
              aria-label="Hide deploy keys panel"
            >
              Hide
            </Button>
          </div>
        </div>

        <p className="rounded border border-yellow-900/60 bg-yellow-900/20 px-3 py-2 text-[11px] text-yellow-200">
          <span className="font-semibold">Caminho 1:</span> revoke removes a
          key from this list, but it does not immediately invalidate the
          credential on the Convex backend (admin keys are stateless on the
          backend). To make a leaked key actually stop working, rotate the
          deployment.
        </p>

        {error && (
          <p className="text-xs text-red-400">
            {error instanceof ApiError ? error.message : "Could not load keys"}
          </p>
        )}
        {actionError && (
          <p className="text-xs text-red-400">{actionError}</p>
        )}

        {isLoading ? (
          <p className="text-xs text-neutral-500">Loading…</p>
        ) : keys.length === 0 ? (
          <p className="text-xs text-neutral-500">
            No deploy keys yet. Create one for each CI integration that
            needs to talk to this deployment.
          </p>
        ) : (
          <div className="overflow-hidden rounded-md border border-neutral-800/80">
            <table className="w-full text-[11px]">
              <thead className="bg-neutral-950 text-neutral-400">
                <tr>
                  <th className="px-3 py-2 text-left font-medium">Name</th>
                  <th className="px-3 py-2 text-left font-medium">Prefix</th>
                  <th className="px-3 py-2 text-left font-medium">
                    Created by
                  </th>
                  <th className="px-3 py-2 text-left font-medium">Created</th>
                  <th className="px-3 py-2 text-right font-medium">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-neutral-900">
                {keys.map((k) => (
                  <tr key={k.id} className="text-neutral-200">
                    <td className="px-3 py-2 font-medium">{k.name}</td>
                    <td className="px-3 py-2 font-mono text-neutral-400">
                      {k.prefix}…
                    </td>
                    <td className="px-3 py-2 text-neutral-400">
                      {k.createdByName || "—"}
                    </td>
                    <td className="px-3 py-2 text-neutral-500">
                      {formatDate(k.createTime)}
                    </td>
                    <td className="px-3 py-2 text-right">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => revoke(k)}
                        disabled={revokingId === k.id}
                        aria-label={`Revoke deploy key ${k.name}`}
                      >
                        {revokingId === k.id ? "Revoking…" : "Revoke"}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        <CreateDeployKeyDialog
          open={createOpen}
          onClose={() => setCreateOpen(false)}
          deploymentName={deploymentName}
          onCreated={onCreated}
        />
      </CardBody>
    </Card>
  );
}

function CreateDeployKeyDialog({
  open,
  onClose,
  deploymentName,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  deploymentName: string;
  onCreated: () => void;
}) {
  const [name, setName] = useState("");
  const [pending, setPending] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [issued, setIssued] = useState<CreateDeployKeyResponse | null>(null);
  const [format, setFormat] = useState<Format>("env");
  const [copied, setCopied] = useState(false);

  // Reset state every time the dialog reopens.
  useEffect(() => {
    if (open) {
      setName("");
      setPending(false);
      setFormError(null);
      setIssued(null);
      setFormat("env");
      setCopied(false);
    }
  }, [open]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    setPending(true);
    try {
      const created = await api.deployments.createDeployKey(
        deploymentName,
        name.trim(),
      );
      setIssued(created);
      onCreated();
    } catch (err) {
      setFormError(
        err instanceof ApiError ? err.message : "Could not create deploy key",
      );
    } finally {
      setPending(false);
    }
  };

  const snippet = issued
    ? format === "env"
      ? issued.envSnippet
      : issued.exportSnippet
    : "";

  const copy = async () => {
    if (!issued) return;
    const ok = await copyToClipboard(snippet);
    if (ok) {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } else {
      setFormError("Could not copy — select the snippet manually and Ctrl+C");
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={issued ? "Deploy key created" : "New deploy key"}
    >
      {!issued && (
        <form onSubmit={submit} className="space-y-4">
          <div className="space-y-2">
            <label
              htmlFor="deploy-key-name"
              className="block text-xs text-neutral-400"
            >
              Name
            </label>
            <Input
              id="deploy-key-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="vercel"
              required
              autoFocus
              maxLength={64}
            />
            <p className="text-xs text-neutral-500">
              A short label so you can recognise which CI integration uses
              this key (e.g. "vercel", "github-actions", "claudin").
            </p>
          </div>
          {formError && <p className="text-xs text-red-400">{formError}</p>}
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="ghost"
              onClick={onClose}
              disabled={pending}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !name.trim()}>
              {pending ? "Creating…" : "Create"}
            </Button>
          </div>
        </form>
      )}

      {issued && (
        <div className="space-y-3">
          <p className="rounded bg-yellow-900/40 px-3 py-2 text-xs text-yellow-200">
            Save this snippet now — you won&apos;t see the full key again.
            If you lose it, revoke this key and create a new one.
          </p>
          <p className="text-xs text-neutral-400">
            Deploy key for{" "}
            <span className="font-medium">{issued.name}</span>:
          </p>
          <div
            className="inline-flex rounded-md border border-neutral-800/80 bg-neutral-950 p-0.5 text-[11px]"
            role="tablist"
            aria-label="Snippet format"
          >
            <button
              type="button"
              role="tab"
              aria-selected={format === "env"}
              className={`rounded px-2 py-1 font-mono transition ${
                format === "env"
                  ? "bg-neutral-800 text-neutral-100"
                  : "text-neutral-400 hover:text-neutral-200"
              }`}
              onClick={() => {
                setFormat("env");
                setCopied(false);
              }}
            >
              .env.production
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={format === "shell"}
              className={`rounded px-2 py-1 font-mono transition ${
                format === "shell"
                  ? "bg-neutral-800 text-neutral-100"
                  : "text-neutral-400 hover:text-neutral-200"
              }`}
              onClick={() => {
                setFormat("shell");
                setCopied(false);
              }}
            >
              shell (export)
            </button>
          </div>
          <pre className="overflow-x-auto whitespace-pre rounded-md border border-neutral-800/80 bg-neutral-950 p-3 font-mono text-[11px] leading-snug text-neutral-200">
            {snippet}
          </pre>
          {formError && <p className="text-xs text-red-400">{formError}</p>}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={copy}>
              {copied ? "Copied!" : "Copy"}
            </Button>
            <Button onClick={onClose}>Done</Button>
          </div>
        </div>
      )}
    </Dialog>
  );
}

function formatDate(iso: string): string {
  if (!iso) return "—";
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return iso;
  }
}
