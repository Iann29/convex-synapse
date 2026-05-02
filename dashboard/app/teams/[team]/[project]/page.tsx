"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { use, useState } from "react";
import useSWR from "swr";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { CliCredentialsPanel } from "@/components/CliCredentialsPanel";
import { EnvVarsPanel } from "@/components/EnvVarsPanel";
import { ProjectMembersPanel } from "@/components/ProjectMembersPanel";
import { TokensPanel } from "@/components/TokensPanel";
import { ApiError, api, type Deployment, type Project, type Team } from "@/lib/api";
import { copyToClipboard } from "@/lib/clipboard";

type Params = { team: string; project: string };

function statusTone(status?: string): "green" | "yellow" | "red" | "neutral" {
  if (!status) return "neutral";
  const s = status.toLowerCase();
  if (s.includes("running") || s === "ready" || s === "active") return "green";
  if (s.includes("provision") || s.includes("pending") || s.includes("creat"))
    return "yellow";
  if (s.includes("fail") || s.includes("error") || s.includes("crash")) return "red";
  return "neutral";
}

export default function ProjectPage({ params }: { params: Promise<Params> }) {
  const { team: teamRef, project: projectId } = use(params);
  const router = useRouter();

  const { data: project, mutate: mutateProject } = useSWR<Project>(
    ["/project", projectId],
    () => api.projects.get(projectId),
  );
  const {
    data: deployments,
    error,
    isLoading,
    mutate,
  } = useSWR<Deployment[]>(
    ["/deployments", projectId],
    () => api.projects.listDeployments(projectId),
    {
      // Poll while any deployment is mid-provisioning (or any other transient
      // state) so the UI catches up without a manual refresh. Once everything
      // is "running" or "deleted" we stop polling to keep the page idle.
      refreshInterval: (latestData) =>
        latestData?.some(
          (d) => d.status !== "running" && d.status !== "deleted",
        )
          ? 2000
          : 0,
    },
  );

  const [open, setOpen] = useState(false);
  const [type, setType] = useState<"dev" | "prod">("dev");
  // HA mode. Off by default — single-replica deployments are the common
  // path. When the backend has SYNAPSE_HA_ENABLED=false (most clusters),
  // submitting with this on returns 400 ha_disabled which we surface
  // inline instead of crashing the dialog.
  const [haMode, setHAMode] = useState(false);
  const [pending, setPending] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [openingName, setOpeningName] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  // Adopt-existing modal state. Kept separate from the create-deployment
  // state because the two forms have different fields and we don't want a
  // submit on one to leak the other's "pending" spinner.
  const [adoptOpen, setAdoptOpen] = useState(false);
  const [adoptUrl, setAdoptUrl] = useState("");
  const [adoptAdminKey, setAdoptAdminKey] = useState("");
  const [adoptType, setAdoptType] = useState<"dev" | "prod">("prod");
  const [adoptName, setAdoptName] = useState("");
  const [adoptPending, setAdoptPending] = useState(false);
  const [adoptError, setAdoptError] = useState<string | null>(null);

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    setPending(true);
    try {
      await api.projects.createDeployment(projectId, {
        type,
        ha: haMode || undefined,
      });
      setOpen(false);
      setHAMode(false);
      await mutate();
    } catch (err) {
      setFormError(
        err instanceof ApiError ? err.message : "Could not create deployment"
      );
    } finally {
      setPending(false);
    }
  };

  const adopt = async (e: React.FormEvent) => {
    e.preventDefault();
    setAdoptError(null);
    setAdoptPending(true);
    try {
      await api.projects.adoptDeployment(projectId, {
        deploymentUrl: adoptUrl.trim(),
        adminKey: adoptAdminKey.trim(),
        deploymentType: adoptType,
        name: adoptName.trim() || undefined,
      });
      setAdoptOpen(false);
      setAdoptUrl("");
      setAdoptAdminKey("");
      setAdoptName("");
      setAdoptType("prod");
      await mutate();
    } catch (err) {
      setAdoptError(
        err instanceof ApiError ? err.message : "Could not adopt deployment"
      );
    } finally {
      setAdoptPending(false);
    }
  };

  const openDashboard = (name: string) => {
    // Open a Synapse-hosted embed shell that iframes the open-source
    // Convex Dashboard and answers its postMessage handshake with
    // adminKey + deploymentUrl. The handshake protocol is the only
    // way to auto-login the dashboard without a rebuild — the URL
    // hash format we tried before (`#deploymentName=...`) is silently
    // ignored by the self-hosted build (verified against the source
    // in get-convex/convex-backend's `npm-packages/dashboard-self-hosted/`).
    window.open(
      `/embed/${encodeURIComponent(name)}`,
      "_blank",
      "noopener,noreferrer",
    );
  };

  const [deletingName, setDeletingName] = useState<string | null>(null);
  const [deletingProject, setDeletingProject] = useState(false);
  const [renameOpen, setRenameOpen] = useState(false);
  const [renameName, setRenameName] = useState("");
  const [renameSlug, setRenameSlug] = useState("");
  const [renamePending, setRenamePending] = useState(false);
  const [copiedName, setCopiedName] = useState<string | null>(null);

  // Transfer dialog state. Loaded lazily (only when the dialog opens) so
  // the projects page doesn't pay the /v1/teams round-trip on every paint.
  const [transferOpen, setTransferOpen] = useState(false);
  const [transferDest, setTransferDest] = useState("");
  const [transferPending, setTransferPending] = useState(false);
  const [transferError, setTransferError] = useState<string | null>(null);
  const { data: myTeams } = useSWR<Team[] | null>(
    transferOpen ? "/teams" : null,
    () => api.teams.list(),
  );
  // Resolve current team to its UUID so we can hide it from the
  // destination dropdown — transferring to the same team is a no-op
  // server-side but a confusing UX option.
  const { data: currentTeam } = useSWR<Team>(
    ["/team", teamRef],
    () => api.teams.get(teamRef),
  );

  const copyUrl = async (name: string, url: string) => {
    const ok = await copyToClipboard(url);
    if (ok) {
      setCopiedName(name);
      // Clear the "Copied!" label after a beat — long enough to be noticed,
      // short enough that re-clicking feels responsive.
      setTimeout(() => {
        setCopiedName((current) => (current === name ? null : current));
      }, 1500);
    } else {
      setActionError("Could not copy URL — select it manually and Ctrl+C");
    }
  };

  const submitRename = async (e: React.FormEvent) => {
    e.preventDefault();
    setActionError(null);
    setRenamePending(true);

    // Build a partial patch — empty name/slug means "leave alone". The PUT
    // endpoint accepts any subset of {name, slug} and 204s on no-op, so a
    // dialog left untouched is harmless to submit.
    const patch: { name?: string; slug?: string } = {};
    if (renameName.trim() && renameName.trim() !== project?.name) {
      patch.name = renameName.trim();
    }
    if (renameSlug.trim() && renameSlug.trim() !== project?.slug) {
      patch.slug = renameSlug.trim();
    }

    try {
      await api.projects.update(projectId, patch);
      setRenameOpen(false);
      // Refresh the project cache so the header updates immediately.
      await mutateProject();
    } catch (err) {
      if (err instanceof ApiError && err.code === "slug_taken") {
        setActionError("Slug already in use by another project in this team.");
      } else if (err instanceof ApiError && err.code === "invalid_slug") {
        setActionError("Slug must be lowercase letters, digits, and dashes only.");
      } else {
        setActionError(
          err instanceof ApiError ? err.message : "Could not rename project",
        );
      }
    } finally {
      setRenamePending(false);
    }
  };

  const submitTransfer = async (e: React.FormEvent) => {
    e.preventDefault();
    setTransferError(null);
    if (!transferDest) {
      setTransferError("Pick a destination team");
      return;
    }
    setTransferPending(true);
    try {
      await api.projects.transfer(projectId, transferDest);
      // Project's team_id flipped — every URL referencing the old team
      // path is stale. Resolve the new team's slug to keep the user on a
      // working page.
      const dest = (myTeams ?? []).find((t) => t.id === transferDest);
      const destSlug = dest?.slug ?? transferDest;
      router.push(`/teams/${encodeURIComponent(destSlug)}/${encodeURIComponent(projectId)}`);
    } catch (err) {
      if (err instanceof ApiError && err.code === "slug_taken") {
        setTransferError(
          "A project with this slug already exists in the destination team.",
        );
      } else if (err instanceof ApiError && err.code === "forbidden") {
        setTransferError("You must be admin of both teams to transfer a project.");
      } else {
        setTransferError(
          err instanceof ApiError ? err.message : "Could not transfer project",
        );
      }
      setTransferPending(false);
    }
  };

  const deleteProject = async () => {
    if (!confirm(`Delete project "${project?.name ?? projectId}"? All its deployments will be removed.`)) {
      return;
    }
    setActionError(null);
    setDeletingProject(true);
    try {
      await api.projects.delete(projectId);
      router.push(`/teams/${encodeURIComponent(teamRef)}`);
    } catch (err) {
      setActionError(
        err instanceof ApiError ? err.message : "Could not delete project"
      );
      setDeletingProject(false);
    }
  };

  const deleteDeployment = async (name: string) => {
    // Confirm via native dialog — the destructive action removes the
    // container and its data volume. Synapse marks the row deleted, then
    // we mutate the SWR cache to drop the row from the list.
    if (!confirm(`Delete deployment "${name}"? Its data volume will be removed.`)) {
      return;
    }
    setActionError(null);
    setDeletingName(name);
    try {
      await api.deployments.delete(name);
      await mutate();
    } catch (err) {
      setActionError(
        err instanceof ApiError ? err.message : "Could not delete deployment"
      );
    } finally {
      setDeletingName(null);
    }
  };

  return (
    <div className="space-y-6">
      <div>
        <nav className="text-xs text-neutral-500">
          <Link href="/teams" className="hover:text-neutral-300">
            Teams
          </Link>{" "}
          /{" "}
          <Link
            href={`/teams/${encodeURIComponent(teamRef)}`}
            className="hover:text-neutral-300"
          >
            {teamRef}
          </Link>{" "}
          / <span className="text-neutral-300">{project?.name ?? projectId}</span>
        </nav>
        <div className="mt-3 flex items-center justify-between">
          <div>
            <h1 className="text-xl font-semibold">{project?.name ?? "Project"}</h1>
            <p className="text-xs text-neutral-400">
              Deployments are real Convex backend containers.
            </p>
          </div>
          <div className="flex gap-2">
            <Button onClick={() => setOpen(true)}>New deployment</Button>
            <Button
              variant="secondary"
              onClick={() => setAdoptOpen(true)}
              aria-label="Adopt existing deployment"
            >
              Adopt existing
            </Button>
            <Button
              variant="secondary"
              onClick={() => {
                setRenameName(project?.name ?? "");
                setRenameSlug(project?.slug ?? "");
                setRenameOpen(true);
              }}
              aria-label="Rename project"
            >
              Rename
            </Button>
            <Button
              variant="secondary"
              onClick={() => {
                setTransferDest("");
                setTransferError(null);
                setTransferOpen(true);
              }}
              aria-label="Transfer project to another team"
              data-testid="project-transfer-open"
            >
              Transfer
            </Button>
            <Button
              variant="danger"
              onClick={deleteProject}
              disabled={deletingProject}
              aria-label="Delete project"
            >
              {deletingProject ? "Deleting…" : "Delete project"}
            </Button>
          </div>
        </div>
      </div>

      {actionError && (
        <p className="rounded border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">
          {actionError}
        </p>
      )}

      {isLoading && (
        <div className="space-y-3">
          {[0, 1, 2].map((i) => (
            <Card key={i}>
              <CardBody className="flex items-center justify-between gap-4">
                <div className="min-w-0 flex-1">
                  <Skeleton className="h-4 w-1/3" />
                  <Skeleton className="mt-2 h-3 w-2/3" />
                </div>
                <div className="flex shrink-0 gap-2">
                  <Skeleton className="h-8 w-28" />
                  <Skeleton className="h-8 w-16" />
                </div>
              </CardBody>
            </Card>
          ))}
        </div>
      )}
      {error && (
        <p className="text-sm text-red-400">
          Failed to load deployments: {(error as Error).message}
        </p>
      )}

      {deployments && deployments.length === 0 && (
        <Card>
          <CardBody className="text-center">
            <p className="text-sm text-neutral-300">No deployments yet.</p>
            <p className="mt-1 text-xs text-neutral-500">
              Provision a dev or prod backend to start running functions.
            </p>
            <Button className="mt-4" onClick={() => setOpen(true)}>
              Create deployment
            </Button>
          </CardBody>
        </Card>
      )}

      {deployments && deployments.length > 0 && (
        <div className="space-y-3">
          {deployments.map((d) => {
            const dtype = d.deploymentType ?? d.type;
            return (
              <Card key={d.name}>
                <CardBody className="flex items-center justify-between gap-4">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <p className="truncate text-sm font-medium text-neutral-100">
                        {d.name}
                      </p>
                      {dtype && (
                        <Badge tone={dtype === "prod" ? "green" : "neutral"}>
                          {dtype}
                        </Badge>
                      )}
                      {d.status && (
                        <Badge tone={statusTone(d.status)}>{d.status}</Badge>
                      )}
                      {d.isDefault && <Badge tone="neutral">default</Badge>}
                      {d.adopted && <Badge tone="neutral">adopted</Badge>}
                      {d.haEnabled && (
                        <Badge tone="green">
                          HA{d.replicaCount ? ` ×${d.replicaCount}` : ""}
                        </Badge>
                      )}
                    </div>
                    {(d.deploymentUrl || d.url) && (
                      <div className="mt-1 flex items-center gap-2">
                        <p className="truncate text-xs text-neutral-500">
                          {d.deploymentUrl || d.url}
                        </p>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() =>
                            copyUrl(d.name, (d.deploymentUrl || d.url) as string)
                          }
                          aria-label="Copy deployment URL"
                        >
                          {copiedName === d.name ? "Copied!" : "Copy"}
                        </Button>
                      </div>
                    )}
                  </div>
                  <div className="flex shrink-0 gap-2">
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => openDashboard(d.name)}
                      disabled={openingName === d.name}
                    >
                      {openingName === d.name ? "Opening..." : "Open dashboard"}
                    </Button>
                    <Button
                      variant="danger"
                      size="sm"
                      onClick={() => deleteDeployment(d.name)}
                      disabled={deletingName === d.name}
                      aria-label={`Delete deployment ${d.name}`}
                    >
                      {deletingName === d.name ? "Deleting..." : "Delete"}
                    </Button>
                  </div>
                </CardBody>
                <div className="border-t border-neutral-900 px-5 py-3">
                  <CliCredentialsPanel deploymentName={d.name} />
                </div>
              </Card>
            );
          })}
        </div>
      )}

      <hr className="border-neutral-900" />
      <EnvVarsPanel projectId={projectId} />

      {/* Project-level RBAC: per-project admin/member/viewer overrides on
          top of team_members. The panel auto-derives the caller's role
          from the listing it fetches, so admin controls only appear for
          actual project admins. */}
      <hr className="border-neutral-900" />
      <ProjectMembersPanel projectId={projectId} />

      {/* Project-scoped + app-scoped tokens. Both are scope=project at the
          access-token table level (well, scope=app for app tokens), but
          they're rendered as separate sections so operators can label
          long-lived service tokens vs short-lived preview deploy keys.
          The TokensPanel component handles the API plumbing per scope. */}
      <hr className="border-neutral-900" />
      <Card>
        <CardBody>
          <TokensPanel scope="project" target={projectId} />
        </CardBody>
      </Card>
      <Card>
        <CardBody>
          <TokensPanel scope="app" target={projectId} />
        </CardBody>
      </Card>


      <Dialog open={open} onClose={() => setOpen(false)} title="Create deployment">
        <form onSubmit={create} className="space-y-4">
          <div className="space-y-2">
            <label className="block text-xs text-neutral-400">Type</label>
            <div className="flex gap-2">
              {(["dev", "prod"] as const).map((t) => (
                <button
                  key={t}
                  type="button"
                  onClick={() => setType(t)}
                  className={`h-9 flex-1 rounded-md border text-sm transition-colors ${
                    type === t
                      ? "border-neutral-300 bg-neutral-800 text-neutral-100"
                      : "border-neutral-700 bg-neutral-900 text-neutral-400 hover:bg-neutral-800"
                  }`}
                >
                  {t}
                </button>
              ))}
            </div>
            <p className="text-xs text-neutral-500">
              Provisions a Convex backend container.
            </p>
          </div>
          <div className="space-y-2">
            <label className="flex items-center gap-2 text-xs text-neutral-400">
              <input
                id="create-ha-toggle"
                type="checkbox"
                checked={haMode}
                onChange={(e) => setHAMode(e.target.checked)}
                className="h-4 w-4 rounded border-neutral-700 bg-neutral-900 text-violet-500 focus:ring-violet-500"
              />
              <span>High availability (2 replicas + Postgres + S3)</span>
            </label>
            {haMode && (
              <p className="text-xs text-neutral-500">
                Requires <code className="text-neutral-300">SYNAPSE_HA_ENABLED=true</code> on
                the cluster plus <code className="text-neutral-300">SYNAPSE_BACKEND_*</code>
                {" "}credentials. See <code className="text-neutral-300">docs/V0_5_PLAN.md</code>.
              </p>
            )}
          </div>
          {formError && <p className="text-xs text-red-400">{formError}</p>}
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="ghost"
              onClick={() => setOpen(false)}
              disabled={pending}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? "Creating..." : "Create"}
            </Button>
          </div>
        </form>
      </Dialog>

      <Dialog
        open={adoptOpen}
        onClose={() => setAdoptOpen(false)}
        title="Adopt existing deployment"
      >
        <form onSubmit={adopt} className="space-y-4">
          <p className="text-xs text-neutral-400">
            Register a Convex backend that&apos;s already running outside Synapse.
            Synapse stores the URL + admin key and skips Docker for delete /
            health on this row — the backend stays under your control.
          </p>
          <div className="space-y-2">
            <label htmlFor="adopt-url" className="block text-xs text-neutral-400">
              Deployment URL
            </label>
            <Input
              id="adopt-url"
              value={adoptUrl}
              onChange={(e) => setAdoptUrl(e.target.value)}
              placeholder="https://convex.example.com:3210"
              required
              autoFocus
            />
          </div>
          <div className="space-y-2">
            <label htmlFor="adopt-admin-key" className="block text-xs text-neutral-400">
              Admin key
            </label>
            <Input
              id="adopt-admin-key"
              type="password"
              value={adoptAdminKey}
              onChange={(e) => setAdoptAdminKey(e.target.value)}
              required
            />
          </div>
          <div className="space-y-2">
            <label className="block text-xs text-neutral-400">Type</label>
            <div className="flex gap-2">
              {(["dev", "prod"] as const).map((t) => (
                <button
                  key={t}
                  type="button"
                  onClick={() => setAdoptType(t)}
                  className={`h-9 flex-1 rounded-md border text-sm transition-colors ${
                    adoptType === t
                      ? "border-neutral-300 bg-neutral-800 text-neutral-100"
                      : "border-neutral-700 bg-neutral-900 text-neutral-400 hover:bg-neutral-800"
                  }`}
                >
                  {t}
                </button>
              ))}
            </div>
          </div>
          <div className="space-y-2">
            <label htmlFor="adopt-name" className="block text-xs text-neutral-400">
              Name <span className="text-neutral-600">(optional — auto-allocated if blank)</span>
            </label>
            <Input
              id="adopt-name"
              value={adoptName}
              onChange={(e) => setAdoptName(e.target.value)}
              placeholder="my-existing-app"
            />
          </div>
          {adoptError && <p className="text-xs text-red-400">{adoptError}</p>}
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="ghost"
              onClick={() => setAdoptOpen(false)}
              disabled={adoptPending}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={adoptPending || !adoptUrl.trim() || !adoptAdminKey.trim()}>
              {adoptPending ? "Verifying…" : "Adopt"}
            </Button>
          </div>
        </form>
      </Dialog>

      <Dialog
        open={renameOpen}
        onClose={() => setRenameOpen(false)}
        title="Rename project"
      >
        <form onSubmit={submitRename} className="space-y-4">
          <div className="space-y-2">
            <label htmlFor="rename-project" className="block text-xs text-neutral-400">
              Name
            </label>
            <Input
              id="rename-project"
              value={renameName}
              onChange={(e) => setRenameName(e.target.value)}
              autoFocus
            />
          </div>
          <div className="space-y-2">
            <label htmlFor="rename-project-slug" className="block text-xs text-neutral-400">
              Slug
            </label>
            <Input
              id="rename-project-slug"
              value={renameSlug}
              onChange={(e) => setRenameSlug(e.target.value)}
              pattern="[a-z0-9-]+"
              title="Lowercase letters, digits, and dashes only"
            />
            <p className="text-xs text-neutral-500">
              Used in URLs. Must be unique within this team.
            </p>
          </div>
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="ghost"
              onClick={() => setRenameOpen(false)}
              disabled={renamePending}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={
                renamePending ||
                (!renameName.trim() && !renameSlug.trim())
              }
              data-testid="project-rename-save"
            >
              {renamePending ? "Saving…" : "Save"}
            </Button>
          </div>
        </form>
      </Dialog>

      <Dialog
        open={transferOpen}
        onClose={() => !transferPending && setTransferOpen(false)}
        title="Transfer project"
      >
        <form onSubmit={submitTransfer} className="space-y-4">
          <p className="text-xs text-neutral-400">
            Move this project (and all its deployments) to another team you
            admin. Refused if a project with the same slug already lives
            there.
          </p>
          <div className="space-y-2">
            <label
              htmlFor="transfer-dest"
              className="block text-xs text-neutral-400"
            >
              Destination team
            </label>
            <select
              id="transfer-dest"
              value={transferDest}
              onChange={(e) => setTransferDest(e.target.value)}
              className="h-9 w-full rounded-md border border-neutral-700 bg-neutral-900 px-3 text-sm text-neutral-100 focus:border-neutral-500 focus:outline-none"
              data-testid="project-transfer-dest"
              required
            >
              <option value="">Pick a team…</option>
              {(myTeams ?? [])
                .filter((t) => t.id !== currentTeam?.id)
                .map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name} ({t.slug})
                  </option>
                ))}
            </select>
          </div>
          {transferError && (
            <p className="text-xs text-red-400">{transferError}</p>
          )}
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="ghost"
              onClick={() => setTransferOpen(false)}
              disabled={transferPending}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={transferPending || !transferDest}
              data-testid="project-transfer-submit"
            >
              {transferPending ? "Transferring…" : "Transfer"}
            </Button>
          </div>
        </form>
      </Dialog>
    </div>
  );
}
