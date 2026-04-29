"use client";

import Link from "next/link";
import { useState, use } from "react";
import useSWR from "swr";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError, api, type Project, type Team } from "@/lib/api";

type Params = { team: string };

export default function TeamPage({ params }: { params: Promise<Params> }) {
  const { team: teamRef } = use(params);

  const { data: team } = useSWR<Team>(["/team", teamRef], () =>
    api.teams.get(teamRef)
  );
  const {
    data: projects,
    error,
    isLoading,
    mutate,
  } = useSWR<Project[]>(["/projects", teamRef], () => api.teams.listProjects(teamRef));

  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [pending, setPending] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    setPending(true);
    try {
      await api.teams.createProject(teamRef, name);
      setName("");
      setOpen(false);
      await mutate();
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : "Could not create project");
    } finally {
      setPending(false);
    }
  };

  return (
    <div className="space-y-6">
      <div>
        <nav className="text-xs text-neutral-500">
          <Link href="/teams" className="hover:text-neutral-300">
            Teams
          </Link>{" "}
          / <span className="text-neutral-300">{team?.name ?? teamRef}</span>
        </nav>
        <div className="mt-3 flex items-center justify-between">
          <div>
            <h1 className="text-xl font-semibold">{team?.name ?? teamRef}</h1>
            <p className="text-xs text-neutral-400">Projects in this team.</p>
          </div>
          <Button onClick={() => setOpen(true)}>New project</Button>
        </div>
      </div>

      {isLoading && (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {[0, 1, 2].map((i) => (
            <Card key={i}>
              <CardBody>
                <Skeleton className="h-4 w-2/3" />
                <Skeleton className="mt-2 h-3 w-1/3" />
              </CardBody>
            </Card>
          ))}
        </div>
      )}
      {error && (
        <p className="text-sm text-red-400">
          Failed to load projects: {(error as Error).message}
        </p>
      )}

      {projects && projects.length === 0 && (
        <Card>
          <CardBody className="text-center">
            <p className="text-sm text-neutral-300">No projects yet.</p>
            <p className="mt-1 text-xs text-neutral-500">
              Each project can host multiple deployments.
            </p>
            <Button className="mt-4" onClick={() => setOpen(true)}>
              Create project
            </Button>
          </CardBody>
        </Card>
      )}

      {projects && projects.length > 0 && (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {projects.map((p) => (
            <Link
              key={p.id}
              href={`/teams/${encodeURIComponent(teamRef)}/${encodeURIComponent(p.id)}`}
              className="group"
            >
              <Card className="transition-colors group-hover:border-neutral-700">
                <CardBody>
                  <p className="text-sm font-medium text-neutral-100">{p.name}</p>
                  <p className="mt-1 text-xs text-neutral-500">{p.slug}</p>
                </CardBody>
              </Card>
            </Link>
          ))}
        </div>
      )}

      <Dialog open={open} onClose={() => setOpen(false)} title="Create project">
        <form onSubmit={create} className="space-y-4">
          <div className="space-y-2">
            <label htmlFor="project-name" className="block text-xs text-neutral-400">
              Project name
            </label>
            <Input
              id="project-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="my-app"
              required
              autoFocus
            />
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
            <Button type="submit" disabled={pending || !name.trim()}>
              {pending ? "Creating..." : "Create"}
            </Button>
          </div>
        </form>
      </Dialog>
    </div>
  );
}
