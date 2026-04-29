"use client";

import Link from "next/link";
import { useState, useMemo, use } from "react";
import useSWR from "swr";
import clsx from "clsx";
import { Avatar } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError, api, type Deployment, type Project, type Team } from "@/lib/api";
import { InvitesPanel } from "@/components/InvitesPanel";

type Params = { team: string };

// Team home — the route a user lands on after picking a team. Mirrors the
// Convex Cloud "Projects" landing: tabs for Projects vs flat Deployments
// list, a search field on the left and create CTA on the right, with a
// grid/list density toggle.
export default function TeamHomePage({ params }: { params: Promise<Params> }) {
  const { team: teamRef } = use(params);

  const [tab, setTab] = useState<"projects" | "deployments">("projects");

  const { data: team } = useSWR<Team>(["/team", teamRef], () =>
    api.teams.get(teamRef),
  );

  return (
    <div className="space-y-6">
      <div className="flex items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-neutral-100">
            {team?.name ?? teamRef}
          </h1>
          <p className="mt-1 text-sm text-neutral-400">
            Projects and deployments owned by this team.
          </p>
        </div>
      </div>

      <SubTabs value={tab} onChange={setTab} />

      {tab === "projects" && <ProjectsView teamRef={teamRef} />}
      {tab === "deployments" && <DeploymentsView teamRef={teamRef} />}

      {/*
        InvitesPanel doubles as the team-management surface on the home page.
        The redesign brief moves it into Team Settings → Members, but the
        Playwright contract issues invites from /teams/<ref> directly, so we
        keep it inline. The Settings → Members page renders the same panel
        for the new IA. */}
      <div className="pt-2">
        <InvitesPanel teamRef={teamRef} />
      </div>
    </div>
  );
}

/* ---------------- Sub-tabs ---------------- */

function SubTabs({
  value,
  onChange,
}: {
  value: "projects" | "deployments";
  onChange: (v: "projects" | "deployments") => void;
}) {
  const items: { id: "projects" | "deployments"; label: string }[] = [
    { id: "projects", label: "Projects" },
    { id: "deployments", label: "Deployments" },
  ];
  return (
    <div className="flex gap-1 border-b border-neutral-900">
      {items.map((it) => (
        <button
          key={it.id}
          type="button"
          onClick={() => onChange(it.id)}
          className={clsx(
            "relative -mb-px h-9 px-3 text-sm transition-colors focus:outline-none",
            value === it.id
              ? "text-neutral-100"
              : "text-neutral-400 hover:text-neutral-200",
          )}
        >
          {it.label}
          {value === it.id && (
            <span
              aria-hidden
              className="absolute inset-x-2.5 -bottom-px h-0.5 rounded-full bg-violet-500"
            />
          )}
        </button>
      ))}
    </div>
  );
}

/* ---------------- Projects tab ---------------- */

type Density = "grid" | "list";

function ProjectsView({ teamRef }: { teamRef: string }) {
  const {
    data: projects,
    error,
    isLoading,
    mutate,
  } = useSWR<Project[]>(["/projects", teamRef], () =>
    api.teams.listProjects(teamRef),
  );

  const [query, setQuery] = useState("");
  const [density, setDensity] = useState<Density>("grid");
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

  const filtered = useMemo(() => {
    if (!projects) return projects;
    const q = query.trim().toLowerCase();
    if (!q) return projects;
    return projects.filter(
      (p) =>
        p.name.toLowerCase().includes(q) ||
        p.slug.toLowerCase().includes(q),
    );
  }, [projects, query]);

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <SearchField value={query} onChange={setQuery} placeholder="Search projects" />
          <DensityToggle value={density} onChange={setDensity} />
        </div>
        <Button onClick={() => setOpen(true)}>New project</Button>
      </div>

      {isLoading && <ProjectsSkeleton density={density} />}
      {error && (
        <p className="text-sm text-red-400">
          Failed to load projects: {(error as Error).message}
        </p>
      )}

      {projects && projects.length === 0 && (
        <ProjectsEmpty onCreate={() => setOpen(true)} />
      )}

      {filtered && filtered.length === 0 && projects && projects.length > 0 && (
        <Card>
          <CardBody className="py-10 text-center text-sm text-neutral-400">
            No projects match <span className="text-neutral-200">"{query}"</span>.
          </CardBody>
        </Card>
      )}

      {filtered && filtered.length > 0 && density === "grid" && (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {filtered.map((p) => (
            <ProjectCard key={p.id} project={p} teamRef={teamRef} />
          ))}
        </div>
      )}

      {filtered && filtered.length > 0 && density === "list" && (
        <Card className="overflow-hidden">
          <ul className="divide-y divide-neutral-800">
            {filtered.map((p) => (
              <li key={p.id}>
                <Link
                  href={`/teams/${encodeURIComponent(teamRef)}/${encodeURIComponent(p.id)}`}
                  className="group flex items-center gap-3 px-5 py-3 transition-colors hover:bg-neutral-900"
                >
                  <Avatar seed={`${teamRef}/${p.slug}`} label={p.name} size="md" />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium text-neutral-100">
                      {p.name}
                    </p>
                    <p className="truncate text-xs text-neutral-500">{p.slug}</p>
                  </div>
                  <Arrow />
                </Link>
              </li>
            ))}
          </ul>
        </Card>
      )}

      <Dialog open={open} onClose={() => setOpen(false)} title="Create project">
        <form onSubmit={create} className="space-y-4">
          <div className="space-y-2">
            <label htmlFor="project-name" className="block text-xs font-medium text-neutral-400">
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
            <p className="text-xs text-neutral-500">
              The slug is generated from the name and stays stable across renames.
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
            <Button type="submit" disabled={pending || !name.trim()}>
              {pending ? "Creating..." : "Create"}
            </Button>
          </div>
        </form>
      </Dialog>
    </div>
  );
}

function ProjectCard({ project, teamRef }: { project: Project; teamRef: string }) {
  return (
    <Link
      href={`/teams/${encodeURIComponent(teamRef)}/${encodeURIComponent(project.id)}`}
      className="group focus:outline-none"
    >
      <Card className="h-full transition-all duration-150 group-hover:border-neutral-700 group-hover:bg-neutral-900/70 group-focus-visible:ring-2 group-focus-visible:ring-violet-400/70">
        <CardBody className="space-y-3">
          <div className="flex items-center gap-2.5">
            <Avatar seed={`${teamRef}/${project.slug}`} label={project.name} size="md" />
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium text-neutral-100">
                {project.name}
              </p>
              <p className="truncate text-xs text-neutral-500">{project.slug}</p>
            </div>
            <Arrow />
          </div>
          <div className="flex items-center gap-1.5 text-[11px] text-neutral-500">
            <DotIcon />
            <span>Open project</span>
          </div>
        </CardBody>
      </Card>
    </Link>
  );
}

function ProjectsEmpty({ onCreate }: { onCreate: () => void }) {
  return (
    <Card>
      <CardBody className="flex flex-col items-center justify-center gap-4 px-6 py-16 text-center">
        <NodesMark />
        <div>
          <p className="text-base font-semibold text-neutral-100">No projects yet.</p>
          <p className="mt-1 max-w-sm text-sm text-neutral-400">
            Each project owns one or more Convex deployments — dev, prod, or both.
          </p>
        </div>
        <Button onClick={onCreate}>Create project</Button>
      </CardBody>
    </Card>
  );
}

function ProjectsSkeleton({ density }: { density: Density }) {
  if (density === "list") {
    return (
      <Card>
        <ul className="divide-y divide-neutral-800">
          {[0, 1, 2].map((i) => (
            <li key={i} className="flex items-center gap-3 px-5 py-3">
              <Skeleton className="h-7 w-7 rounded-full" />
              <div className="flex-1">
                <Skeleton className="h-3.5 w-1/3" />
                <Skeleton className="mt-2 h-3 w-1/4" />
              </div>
            </li>
          ))}
        </ul>
      </Card>
    );
  }
  return (
    <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {[0, 1, 2].map((i) => (
        <Card key={i}>
          <CardBody>
            <div className="flex items-center gap-2.5">
              <Skeleton className="h-7 w-7 rounded-full" />
              <div className="flex-1">
                <Skeleton className="h-3.5 w-2/3" />
                <Skeleton className="mt-2 h-3 w-1/3" />
              </div>
            </div>
          </CardBody>
        </Card>
      ))}
    </div>
  );
}

/* ---------------- Deployments tab ---------------- */

function DeploymentsView({ teamRef }: { teamRef: string }) {
  const { data, error, isLoading } = useSWR<Deployment[]>(
    ["/team-deployments", teamRef],
    () => api.teams.listDeployments(teamRef),
    {
      refreshInterval: (latest) =>
        latest?.some((d) => d.status !== "running" && d.status !== "deleted")
          ? 2000
          : 0,
    },
  );
  const { data: projects } = useSWR<Project[]>(["/projects", teamRef], () =>
    api.teams.listProjects(teamRef),
  );

  const [query, setQuery] = useState("");

  const filtered = useMemo(() => {
    if (!data) return data;
    const q = query.trim().toLowerCase();
    if (!q) return data;
    return data.filter(
      (d) =>
        d.name.toLowerCase().includes(q) ||
        (d.deploymentUrl ?? d.url ?? "").toLowerCase().includes(q),
    );
  }, [data, query]);

  const projectName = (id?: string) =>
    projects?.find((p) => p.id === id)?.name;

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <SearchField value={query} onChange={setQuery} placeholder="Search deployments" />
      </div>

      {isLoading && (
        <Card>
          <ul className="divide-y divide-neutral-800">
            {[0, 1, 2].map((i) => (
              <li key={i} className="flex items-center gap-3 px-5 py-3">
                <Skeleton className="h-3.5 w-1/4" />
                <Skeleton className="ml-auto h-3 w-1/3" />
              </li>
            ))}
          </ul>
        </Card>
      )}
      {error && (
        <p className="text-sm text-red-400">
          Failed to load deployments: {(error as Error).message}
        </p>
      )}

      {data && data.length === 0 && (
        <Card>
          <CardBody className="flex flex-col items-center justify-center gap-3 px-6 py-12 text-center">
            <NodesMark />
            <p className="text-sm font-medium text-neutral-200">
              No deployments yet.
            </p>
            <p className="max-w-sm text-xs text-neutral-500">
              Deployments show up here as you provision them inside a project.
            </p>
          </CardBody>
        </Card>
      )}

      {filtered && filtered.length === 0 && data && data.length > 0 && (
        <Card>
          <CardBody className="py-10 text-center text-sm text-neutral-400">
            No deployments match <span className="text-neutral-200">"{query}"</span>.
          </CardBody>
        </Card>
      )}

      {filtered && filtered.length > 0 && (
        <Card className="overflow-hidden">
          <ul className="divide-y divide-neutral-800">
            {filtered.map((d) => {
              const dtype = d.deploymentType ?? d.type;
              const url = d.deploymentUrl ?? d.url;
              return (
                <li
                  key={d.id ?? d.name}
                  className="flex items-center gap-4 px-5 py-3"
                >
                  <Link
                    href={
                      d.projectId
                        ? `/teams/${encodeURIComponent(teamRef)}/${encodeURIComponent(d.projectId)}`
                        : `/teams/${encodeURIComponent(teamRef)}`
                    }
                    className="min-w-0 flex-1 truncate text-sm font-medium text-neutral-100 hover:text-violet-300 hover:underline"
                  >
                    {d.name}
                  </Link>
                  <div className="flex items-center gap-2">
                    {dtype && (
                      <Badge tone={dtype === "prod" ? "green" : "neutral"}>
                        {dtype}
                      </Badge>
                    )}
                    {d.status && (
                      <Badge tone={statusTone(d.status)}>{d.status}</Badge>
                    )}
                  </div>
                  <span className="hidden min-w-0 max-w-[200px] truncate text-xs text-neutral-500 md:inline">
                    {projectName(d.projectId) ?? d.projectId ?? ""}
                  </span>
                  <span className="hidden min-w-0 max-w-[260px] truncate font-mono text-[11px] text-neutral-500 lg:inline">
                    {url ?? ""}
                  </span>
                </li>
              );
            })}
          </ul>
        </Card>
      )}
    </div>
  );
}

/* ---------------- Helpers ---------------- */

function statusTone(status?: string): "green" | "yellow" | "red" | "neutral" {
  if (!status) return "neutral";
  const s = status.toLowerCase();
  if (s.includes("running") || s === "ready" || s === "active") return "green";
  if (s.includes("provision") || s.includes("pending") || s.includes("creat"))
    return "yellow";
  if (s.includes("fail") || s.includes("error") || s.includes("crash")) return "red";
  return "neutral";
}

function SearchField({
  value,
  onChange,
  placeholder,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
}) {
  return (
    <div className="relative">
      <SearchIcon />
      <input
        type="search"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="h-9 w-64 rounded-md border border-neutral-800 bg-neutral-900 pl-8 pr-3 text-sm text-neutral-100 placeholder:text-neutral-500 focus:outline-none focus-visible:border-violet-500/60 focus-visible:ring-2 focus-visible:ring-violet-400/30"
      />
    </div>
  );
}

function DensityToggle({
  value,
  onChange,
}: {
  value: Density;
  onChange: (v: Density) => void;
}) {
  return (
    <div
      role="group"
      aria-label="View density"
      className="inline-flex h-9 rounded-md border border-neutral-800 bg-neutral-900 p-0.5"
    >
      <button
        type="button"
        onClick={() => onChange("grid")}
        aria-pressed={value === "grid"}
        aria-label="Grid view"
        className={clsx(
          "flex h-full w-8 items-center justify-center rounded transition-colors",
          value === "grid"
            ? "bg-neutral-800 text-neutral-100"
            : "text-neutral-500 hover:text-neutral-200",
        )}
      >
        <GridIcon />
      </button>
      <button
        type="button"
        onClick={() => onChange("list")}
        aria-pressed={value === "list"}
        aria-label="List view"
        className={clsx(
          "flex h-full w-8 items-center justify-center rounded transition-colors",
          value === "list"
            ? "bg-neutral-800 text-neutral-100"
            : "text-neutral-500 hover:text-neutral-200",
        )}
      >
        <ListIcon />
      </button>
    </div>
  );
}

/* ---------------- Inline icons / decoration ---------------- */

function SearchIcon() {
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 16 16"
      aria-hidden
      className="absolute left-2.5 top-1/2 -translate-y-1/2 text-neutral-500"
    >
      <circle cx="7" cy="7" r="4.5" stroke="currentColor" strokeWidth="1.5" fill="none" />
      <path d="M10.5 10.5 13 13" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
  );
}

function GridIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden>
      <rect x="2" y="2" width="5" height="5" rx="1" fill="currentColor" />
      <rect x="9" y="2" width="5" height="5" rx="1" fill="currentColor" />
      <rect x="2" y="9" width="5" height="5" rx="1" fill="currentColor" />
      <rect x="9" y="9" width="5" height="5" rx="1" fill="currentColor" />
    </svg>
  );
}

function ListIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden>
      <rect x="2" y="3" width="12" height="2" rx="0.5" fill="currentColor" />
      <rect x="2" y="7" width="12" height="2" rx="0.5" fill="currentColor" />
      <rect x="2" y="11" width="12" height="2" rx="0.5" fill="currentColor" />
    </svg>
  );
}

function Arrow() {
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 16 16"
      aria-hidden
      className="text-neutral-600 transition-transform duration-150 group-hover:translate-x-0.5 group-hover:text-neutral-300"
    >
      <path d="M5 3l5 5-5 5" stroke="currentColor" strokeWidth="1.6" fill="none" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function DotIcon() {
  return (
    <svg width="6" height="6" viewBox="0 0 6 6" aria-hidden>
      <circle cx="3" cy="3" r="2" fill="currentColor" className="text-neutral-700" />
    </svg>
  );
}

function NodesMark() {
  return (
    <svg
      width="56"
      height="56"
      viewBox="0 0 64 64"
      aria-hidden
      className="text-neutral-700"
    >
      <defs>
        <linearGradient id="empty-grad" x1="0" y1="0" x2="64" y2="64" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#22d3ee" />
          <stop offset="1" stopColor="#a855f7" />
        </linearGradient>
      </defs>
      <g stroke="url(#empty-grad)" strokeWidth="1.5" strokeLinecap="round" opacity="0.7">
        <line x1="14" y1="18" x2="32" y2="32" />
        <line x1="50" y1="18" x2="32" y2="32" />
        <line x1="32" y1="50" x2="32" y2="32" />
      </g>
      <g fill="url(#empty-grad)">
        <circle cx="14" cy="18" r="3" />
        <circle cx="50" cy="18" r="3" />
        <circle cx="32" cy="50" r="3" />
      </g>
      <circle cx="32" cy="32" r="4.5" fill="#0a0a0a" stroke="url(#empty-grad)" strokeWidth="1.5" />
    </svg>
  );
}
