"use client";

import Link from "next/link";
import { useState } from "react";
import useSWR from "swr";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError, api, type Team } from "@/lib/api";

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
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Teams</h1>
          <p className="text-xs text-neutral-400">
            A team owns projects and members.
          </p>
        </div>
        <Button onClick={() => setOpen(true)}>New team</Button>
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
          Failed to load teams: {(error as Error).message}
        </p>
      )}

      {data && data.length === 0 && (
        <Card>
          <CardBody className="text-center">
            <p className="text-sm text-neutral-300">No teams yet.</p>
            <p className="mt-1 text-xs text-neutral-500">
              Create one to get started.
            </p>
            <Button className="mt-4" onClick={() => setOpen(true)}>
              Create team
            </Button>
          </CardBody>
        </Card>
      )}

      {data && data.length > 0 && (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {data.map((t) => (
            <Link
              key={t.id}
              href={`/teams/${encodeURIComponent(t.slug)}`}
              className="group"
            >
              <Card className="transition-colors group-hover:border-neutral-700">
                <CardBody>
                  <p className="text-sm font-medium text-neutral-100">{t.name}</p>
                  <p className="mt-1 text-xs text-neutral-500">{t.slug}</p>
                </CardBody>
              </Card>
            </Link>
          ))}
        </div>
      )}

      <Dialog open={open} onClose={() => setOpen(false)} title="Create team">
        <form onSubmit={create} className="space-y-4">
          <div className="space-y-2">
            <label htmlFor="team-name" className="block text-xs text-neutral-400">
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
