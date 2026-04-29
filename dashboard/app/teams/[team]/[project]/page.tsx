"use client";

import Link from "next/link";
import { use, useState } from "react";
import useSWR from "swr";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { ApiError, api, type Deployment, type Project } from "@/lib/api";

type Params = { team: string; project: string };

const CONVEX_DASHBOARD_URL =
  process.env.NEXT_PUBLIC_CONVEX_DASHBOARD_URL?.replace(/\/$/, "") ||
  "http://localhost:6791";

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

  const { data: project } = useSWR<Project>(["/project", projectId], () =>
    api.projects.get(projectId)
  );
  const {
    data: deployments,
    error,
    isLoading,
    mutate,
  } = useSWR<Deployment[]>(["/deployments", projectId], () =>
    api.projects.listDeployments(projectId)
  );

  const [open, setOpen] = useState(false);
  const [type, setType] = useState<"dev" | "prod">("dev");
  const [pending, setPending] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [openingName, setOpeningName] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    setPending(true);
    try {
      await api.projects.createDeployment(projectId, { type });
      setOpen(false);
      await mutate();
    } catch (err) {
      setFormError(
        err instanceof ApiError ? err.message : "Could not create deployment"
      );
    } finally {
      setPending(false);
    }
  };

  const openDashboard = async (name: string) => {
    setActionError(null);
    setOpeningName(name);
    try {
      const auth = await api.deployments.auth(name);
      const url = `${CONVEX_DASHBOARD_URL}/#deploymentName=${encodeURIComponent(
        auth.deploymentName
      )}&adminKey=${encodeURIComponent(auth.adminKey)}&deploymentUrl=${encodeURIComponent(
        auth.deploymentUrl
      )}`;
      window.open(url, "_blank", "noopener,noreferrer");
    } catch (err) {
      setActionError(
        err instanceof ApiError ? err.message : "Could not load deployment credentials"
      );
    } finally {
      setOpeningName(null);
    }
  };

  const [deletingName, setDeletingName] = useState<string | null>(null);

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
          <Button onClick={() => setOpen(true)}>New deployment</Button>
        </div>
      </div>

      {actionError && (
        <p className="rounded border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">
          {actionError}
        </p>
      )}

      {isLoading && <p className="text-sm text-neutral-500">Loading deployments...</p>}
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
                    </div>
                    {(d.deploymentUrl || d.url) && (
                      <p className="mt-1 truncate text-xs text-neutral-500">
                        {d.deploymentUrl || d.url}
                      </p>
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
              </Card>
            );
          })}
        </div>
      )}

      {/* TODO: settings page (rename / delete project, env vars). */}

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
    </div>
  );
}
