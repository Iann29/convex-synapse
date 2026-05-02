"use client";

import { useRouter } from "next/navigation";
import { useEffect, useState, use } from "react";
import useSWR from "swr";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError, api, type Team } from "@/lib/api";
import { copyToClipboard } from "@/lib/clipboard";

type Params = { team: string };

// General team settings.
//
// Surfaces:
//   - Identity card (name + slug + immutable id, copy slug)
//   - Edit team form (name + slug + defaultRegion, hits POST /v1/teams/{ref})
//   - Delete-team danger zone (POST /v1/teams/{ref}/delete; refused with
//     409 team_has_deployments while live deployments exist).
export default function TeamGeneralPage({
  params,
}: {
  params: Promise<Params>;
}) {
  const { team: teamRef } = use(params);
  const { data: team, isLoading, mutate } = useSWR<Team>(
    ["/team", teamRef],
    () => api.teams.get(teamRef),
  );

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle>Identity</CardTitle>
          <CardDescription>
            The team&apos;s display name, slug, and immutable id.
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
              <dd className="text-neutral-100" data-testid="team-name">
                {team.name}
              </dd>
              <dt className="text-neutral-500">Slug</dt>
              <dd className="flex items-center gap-2">
                <code
                  className="rounded bg-neutral-900 px-2 py-0.5 font-mono text-xs text-neutral-200"
                  data-testid="team-slug"
                >
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

      {team && <EditCard team={team} onSaved={(next) => mutate(next)} />}

      {team && <DangerCard team={team} />}
    </div>
  );
}

function CopySlugButton({ slug }: { slug: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    const ok = await copyToClipboard(slug);
    if (ok) {
      setCopied(true);
      const t = setTimeout(() => setCopied(false), 1500);
      return () => clearTimeout(t);
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

function EditCard({
  team,
  onSaved,
}: {
  team: Team;
  onSaved: (next: Team) => void;
}) {
  const router = useRouter();
  const [name, setName] = useState(team.name);
  const [slug, setSlug] = useState(team.slug);
  const [region, setRegion] = useState(
    team.defaultRegion ?? "",
  );
  const [pending, setPending] = useState(false);
  const [err, setErr] = useState<{ code?: string; msg: string } | null>(null);

  // Resync local state when the upstream team changes (e.g. after a slug
  // rename in another tab). Without this the form would re-render with
  // stale defaults.
  useEffect(() => {
    setName(team.name);
    setSlug(team.slug);
    setRegion(team.defaultRegion ?? "");
  }, [team]);

  const dirty =
    name.trim() !== team.name ||
    slug.trim() !== team.slug ||
    region.trim() !==
      (team.defaultRegion ?? "");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr(null);
    if (!dirty) return;
    setPending(true);

    const patch: { name?: string; slug?: string; defaultRegion?: string } = {};
    if (name.trim() !== team.name) patch.name = name.trim();
    if (slug.trim() !== team.slug) patch.slug = slug.trim();
    if (
      region.trim() !==
      (team.defaultRegion ?? "")
    ) {
      patch.defaultRegion = region.trim();
    }

    try {
      const next = await api.teams.update(team.slug, patch);
      onSaved(next);
      // If the slug changed the URL we're on no longer resolves — push to
      // the new settings URL so subsequent reads / refreshes keep working.
      if (patch.slug && patch.slug !== team.slug) {
        router.replace(`/teams/${encodeURIComponent(patch.slug)}/settings/general`);
      }
    } catch (e) {
      if (e instanceof ApiError) {
        setErr({ code: e.code, msg: e.message });
      } else {
        setErr({ msg: "Could not save team" });
      }
    } finally {
      setPending(false);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>Edit team</CardTitle>
        <CardDescription>
          Rename or re-slug your team and set the default region.
        </CardDescription>
      </CardHeader>
      <CardBody>
        <form onSubmit={submit} className="space-y-4">
          <div className="space-y-2">
            <label
              htmlFor="settings-team-name"
              className="block text-xs font-medium text-neutral-400"
            >
              Team name
            </label>
            <Input
              id="settings-team-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
              maxLength={120}
            />
          </div>
          <div className="space-y-2">
            <label
              htmlFor="settings-team-slug"
              className="block text-xs font-medium text-neutral-400"
            >
              Slug
            </label>
            <Input
              id="settings-team-slug"
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              pattern="[a-z0-9-]+"
              title="Lowercase letters, digits, and dashes only"
              required
            />
            <p className="text-xs text-neutral-500">
              Used in URLs. Lowercase letters, digits, and dashes only.
              Changing this updates the URL of every page in this team.
            </p>
          </div>
          <div className="space-y-2">
            <label
              htmlFor="settings-team-region"
              className="block text-xs font-medium text-neutral-400"
            >
              Default region
            </label>
            <Input
              id="settings-team-region"
              value={region}
              onChange={(e) => setRegion(e.target.value)}
              placeholder="self-hosted"
            />
            <p className="text-xs text-neutral-500">
              Stored for parity with Convex Cloud — Synapse self-hosted
              is single-region today, so this is informational only.
            </p>
          </div>
          {err && (
            <div className="rounded border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">
              {err.code === "slug_taken" ? (
                <p>That slug is already in use by another team.</p>
              ) : err.code === "invalid_slug" ? (
                <p>Slug must contain only lowercase letters, digits, and dashes.</p>
              ) : (
                <p>{err.msg}</p>
              )}
            </div>
          )}
          <div className="flex items-center justify-end gap-2">
            <Button
              type="submit"
              disabled={pending || !dirty}
              data-testid="team-save"
            >
              {pending ? "Saving…" : "Save"}
            </Button>
          </div>
        </form>
      </CardBody>
    </Card>
  );
}

function DangerCard({ team }: { team: Team }) {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [confirm, setConfirm] = useState("");
  const [pending, setPending] = useState(false);
  const [err, setErr] = useState<{ code?: string; msg: string } | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr(null);
    setPending(true);
    try {
      await api.teams.delete(team.slug);
      router.replace("/teams");
    } catch (e) {
      if (e instanceof ApiError) {
        setErr({ code: e.code, msg: e.message });
      } else {
        setErr({ msg: "Could not delete team" });
      }
    } finally {
      setPending(false);
    }
  };

  return (
    <Card className="border-red-500/30 bg-red-500/[0.04]">
      <CardHeader className="border-red-500/20">
        <CardTitle className="text-red-200">Delete team</CardTitle>
        <CardDescription className="text-red-300/70">
          Irrevocably remove this team along with its projects and members.
          Refused while any deployment in this team is still live —
          delete or transfer those first.
        </CardDescription>
      </CardHeader>
      <CardBody className="flex items-center justify-between gap-3">
        <p className="text-xs text-red-300/70">
          Cascades to projects, env vars, invites, and audit events.
        </p>
        <Button
          variant="danger"
          onClick={() => {
            setOpen(true);
            setConfirm("");
            setErr(null);
          }}
          data-testid="team-delete-open"
          aria-label="Delete team"
        >
          Delete team
        </Button>
      </CardBody>

      <Dialog
        open={open}
        onClose={() => !pending && setOpen(false)}
        title="Delete team"
      >
        <form onSubmit={submit} className="space-y-4">
          <p className="text-sm text-neutral-300">
            Type the team name <strong>{team.name}</strong> to confirm.
          </p>
          <Input
            id="team-delete-confirm"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            placeholder={team.name}
            autoFocus
          />
          {err && (
            <div className="rounded border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">
              {err.code === "team_has_deployments" ? (
                <p>
                  This team still owns live deployments. Delete or transfer
                  them first, then retry.
                </p>
              ) : (
                <p>{err.msg}</p>
              )}
            </div>
          )}
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="ghost"
              onClick={() => setOpen(false)}
              disabled={pending}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              variant="danger"
              disabled={pending || confirm !== team.name}
              data-testid="team-delete-confirm"
            >
              {pending ? "Deleting…" : "Delete team"}
            </Button>
          </div>
        </form>
      </Dialog>
    </Card>
  );
}
