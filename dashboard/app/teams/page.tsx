"use client";

import Link from "next/link";
import { useState } from "react";
import useSWR from "swr";
import { Avatar } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError, api, type Team } from "@/lib/api";

// Top-level teams listing — entry point after login. Surfaced through the
// team picker's "All teams · Create new" footer too. Everything else lives
// under /teams/<ref>/…
export default function TeamsPage() {
  const { data, error, isLoading, mutate } = useSWR<Team[]>("/teams", () =>
    api.teams.list()
  );

  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [pending, setPending] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    setPending(true);
    try {
      await api.teams.create(name);
      setName("");
      setOpen(false);
      await mutate();
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : "Could not create team");
    } finally {
      setPending(false);
    }
  };

  return (
    <div className="space-y-8">
      <div className="flex items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-neutral-100">
            Teams
          </h1>
          <p className="mt-1 text-sm text-neutral-400">
            A team owns projects, members, and deployments.
          </p>
        </div>
        <Button onClick={() => setOpen(true)}>New team</Button>
      </div>

      {isLoading && (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[0, 1, 2].map((i) => (
            <Card key={i}>
              <CardBody className="flex items-center gap-3">
                <Skeleton className="h-10 w-10 rounded-full" />
                <div className="flex-1">
                  <Skeleton className="h-4 w-2/3" />
                  <Skeleton className="mt-2 h-3 w-1/3" />
                </div>
              </CardBody>
            </Card>
          ))}
        </div>
      )}
      {error && (
        <p className="text-sm text-red-400">
          Failed to load teams: {(error as Error).message}
        </p>
      )}

      {data && data.length === 0 && (
        <EmptyState onCreate={() => setOpen(true)} />
      )}

      {data && data.length > 0 && (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {data.map((t) => (
            <Link
              key={t.id}
              href={`/teams/${encodeURIComponent(t.slug)}`}
              className="group focus:outline-none"
            >
              <Card className="h-full transition-all duration-150 group-hover:border-neutral-700 group-hover:bg-neutral-900/70 group-focus-visible:ring-2 group-focus-visible:ring-violet-400/70">
                <CardBody className="flex items-center gap-3">
                  <Avatar seed={t.slug} label={t.name} size="lg" />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium text-neutral-100">
                      {t.name}
                    </p>
                    <p className="mt-0.5 truncate text-xs text-neutral-500">
                      {t.slug}
                    </p>
                  </div>
                  <Arrow />
                </CardBody>
              </Card>
            </Link>
          ))}
        </div>
      )}

      <Dialog open={open} onClose={() => setOpen(false)} title="Create team">
        <form onSubmit={create} className="space-y-4">
          <div className="space-y-2">
            <label htmlFor="team-name" className="block text-xs font-medium text-neutral-400">
              Team name
            </label>
            <Input
              id="team-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Acme Inc."
              required
              autoFocus
            />
            <p className="text-xs text-neutral-500">
              You can rename it later — the slug is locked once created.
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

function EmptyState({ onCreate }: { onCreate: () => void }) {
  return (
    <Card className="overflow-hidden">
      <CardBody className="flex flex-col items-center justify-center gap-4 px-6 py-16 text-center">
        <div className="relative">
          <div
            aria-hidden
            className="absolute inset-0 -z-10 scale-150 rounded-full bg-violet-500/10 blur-2xl"
          />
          <Constellation />
        </div>
        <div>
          <p className="text-base font-semibold text-neutral-100">No teams yet.</p>
          <p className="mt-1 max-w-sm text-sm text-neutral-400">
            Teams are how you group projects, invite collaborators, and host
            deployments. Create one to get started.
          </p>
        </div>
        <Button onClick={onCreate}>Create team</Button>
      </CardBody>
    </Card>
  );
}

function Constellation() {
  // Decorative-only — same node-cluster motif as the logo, scaled up for
  // empty states. Pure SVG, no asset to ship.
  return (
    <svg
      width="64"
      height="64"
      viewBox="0 0 64 64"
      aria-hidden
      className="text-neutral-700"
    >
      <defs>
        <linearGradient id="es-grad" x1="0" y1="0" x2="64" y2="64" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#22d3ee" />
          <stop offset="1" stopColor="#a855f7" />
        </linearGradient>
      </defs>
      <g stroke="url(#es-grad)" strokeWidth="1.5" strokeLinecap="round" opacity="0.7">
        <line x1="14" y1="18" x2="32" y2="32" />
        <line x1="50" y1="18" x2="32" y2="32" />
        <line x1="14" y1="50" x2="32" y2="32" />
        <line x1="50" y1="50" x2="32" y2="32" />
      </g>
      <g fill="url(#es-grad)">
        <circle cx="14" cy="18" r="3" />
        <circle cx="50" cy="18" r="3" />
        <circle cx="14" cy="50" r="3" />
        <circle cx="50" cy="50" r="3" />
      </g>
      <circle cx="32" cy="32" r="4.5" fill="#0a0a0a" stroke="url(#es-grad)" strokeWidth="1.5" />
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
