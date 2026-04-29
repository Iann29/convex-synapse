"use client";

import { useEffect, useState, use } from "react";
import useSWR from "swr";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { api, type Team } from "@/lib/api";

type Params = { team: string };

// General team settings. v0 surfaces:
//   - Read-only identity card (name + slug + copy-to-clipboard)
//   - "Edit team" card with a name input (rename endpoint isn't implemented
//     server-side yet — the action is wired so it'll start working once
//     PUT /v1/teams/<ref> ships, but for now we mark it disabled).
//   - Delete-team danger zone, also disabled until the backend lands.
// Disabled-but-visible matches the Convex Cloud aesthetic and signals the
// roadmap without us having to write fake endpoints.
export default function TeamGeneralPage({
  params,
}: {
  params: Promise<Params>;
}) {
  const { team: teamRef } = use(params);
  const { data: team, isLoading } = useSWR<Team>(["/team", teamRef], () =>
    api.teams.get(teamRef),
  );

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle>Identity</CardTitle>
          <CardDescription>
            The team's display name, slug, and immutable id.
          </CardDescription>
        </CardHeader>
        <CardBody className="space-y-4">
          {isLoading && (
            <div className="space-y-2">
              <Skeleton className="h-4 w-1/3" />
              <Skeleton className="h-4 w-1/4" />
            </div>
          )}
          {team && (
            <dl className="grid grid-cols-[6rem_1fr] gap-y-3 text-sm">
              <dt className="text-neutral-500">Name</dt>
              <dd className="text-neutral-100">{team.name}</dd>
              <dt className="text-neutral-500">Slug</dt>
              <dd className="flex items-center gap-2">
                <code className="rounded bg-neutral-900 px-2 py-0.5 font-mono text-xs text-neutral-200">
                  {team.slug}
                </code>
                <CopySlugButton slug={team.slug} />
              </dd>
              <dt className="text-neutral-500">Team id</dt>
              <dd className="font-mono text-xs text-neutral-300">{team.id}</dd>
            </dl>
          )}
        </CardBody>
      </Card>

      <RenameCard team={team} />

      <DangerCard />
    </div>
  );
}

function CopySlugButton({ slug }: { slug: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(slug);
      setCopied(true);
      const t = setTimeout(() => setCopied(false), 1500);
      return () => clearTimeout(t);
    } catch {
      // Insecure origins can't reach the clipboard — just no-op.
    }
  };
  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={copy}
      aria-label="Copy team slug"
    >
      {copied ? "Copied!" : "Copy"}
    </Button>
  );
}

function RenameCard({ team }: { team?: Team }) {
  const [value, setValue] = useState("");
  useEffect(() => {
    if (team) setValue(team.name);
  }, [team]);

  // No PUT /v1/teams/<ref> in v0 yet — keep the form visually present so
  // the IA matches Cloud, but mark Save disabled with a friendly note.
  const dirty = !!team && value.trim() !== "" && value.trim() !== team.name;

  return (
    <Card>
      <CardHeader>
        <CardTitle>Edit team</CardTitle>
        <CardDescription>
          Rename your team. The slug stays the same.
        </CardDescription>
      </CardHeader>
      <CardBody className="space-y-4">
        <div className="space-y-2">
          <label
            htmlFor="settings-team-name"
            className="block text-xs font-medium text-neutral-400"
          >
            Team name
          </label>
          <Input
            id="settings-team-name"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            disabled={!team}
          />
        </div>
        <div className="flex items-center justify-between gap-3">
          <p className="text-xs text-neutral-500">
            Renaming requires a backend endpoint that isn't shipped yet — the
            field is here so the surface is ready when it lands.
          </p>
          <Button disabled={!dirty} title="Backend rename endpoint pending">
            Save
          </Button>
        </div>
      </CardBody>
    </Card>
  );
}

function DangerCard() {
  return (
    <Card className="border-red-500/30 bg-red-500/[0.04]">
      <CardHeader className="border-red-500/20">
        <CardTitle className="text-red-200">Delete team</CardTitle>
        <CardDescription className="text-red-300/70">
          Irrevocably remove this team, its projects, and all deployments.
        </CardDescription>
      </CardHeader>
      <CardBody className="flex items-center justify-between gap-3">
        <p className="text-xs text-red-300/70">
          Disabled in v0 — Synapse doesn't expose a team-delete endpoint yet.
        </p>
        <Button variant="danger" disabled>
          Delete team
        </Button>
      </CardBody>
    </Card>
  );
}
