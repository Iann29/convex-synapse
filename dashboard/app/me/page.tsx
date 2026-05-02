"use client";

import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import useSWR from "swr";
import { Avatar } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { TokensPanel } from "@/components/TokensPanel";
import { ApiError, api } from "@/lib/api";
import { clearAuth, type User } from "@/lib/auth";

// /me — the authenticated user's account page. Identity card up top,
// personal-access-token panel below. Profile edits + account deletion
// land here too — the cloud-spec endpoints are also exposed under
// /v1/me/* so this page has the canonical edit surface.
export default function MePage() {
  const { data, error, isLoading, mutate } = useSWR<User>("/me", () => api.me());

  return (
    <div className="space-y-8">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight text-neutral-100">
          Account
        </h1>
        <p className="mt-1 text-sm text-neutral-400">
          Your Synapse identity and API access tokens.
        </p>
      </header>

      <Card>
        <CardHeader>
          <CardTitle>Profile</CardTitle>
          <CardDescription>
            Visible to teammates inside any team you belong to.
          </CardDescription>
        </CardHeader>
        <CardBody>
          {isLoading && (
            <div className="flex items-center gap-4">
              <Skeleton className="h-12 w-12 rounded-full" />
              <div className="flex-1 space-y-2">
                <Skeleton className="h-4 w-1/3" />
                <Skeleton className="h-3 w-1/4" />
              </div>
            </div>
          )}
          {error && (
            <p className="text-xs text-red-400">
              Failed to load profile: {(error as Error).message}
            </p>
          )}
          {data && <ProfileForm user={data} onSaved={() => mutate()} />}
        </CardBody>
      </Card>

      <Card>
        <CardBody>
          <TokensPanel />
        </CardBody>
      </Card>

      {data && <DangerZone user={data} />}
    </div>
  );
}

// ProfileForm renders the avatar + identity dl plus an inline-edit
// "name" affordance. The email and id are immutable; only the display
// name flows through update_profile_name.
function ProfileForm({ user, onSaved }: { user: User; onSaved: () => void }) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(user.name ?? "");
  const [pending, setPending] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // Reset the local field whenever the upstream user changes (e.g. on first
  // SWR fill or after a cross-tab edit). Keeps the input in sync if the user
  // closes the form without saving.
  useEffect(() => {
    if (!editing) setValue(user.name ?? "");
  }, [user.name, editing]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr(null);
    if (!value.trim() || value.trim() === user.name) {
      setEditing(false);
      return;
    }
    setPending(true);
    try {
      await api.updateProfileName(value.trim());
      setEditing(false);
      onSaved();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : "Could not save");
    } finally {
      setPending(false);
    }
  };

  return (
    <div className="flex flex-col gap-5 sm:flex-row sm:items-center">
      <Avatar
        seed={user.email}
        label={user.name || user.email}
        size="lg"
        className="h-14 w-14 text-lg"
      />
      <dl className="grid flex-1 grid-cols-[6rem_1fr] gap-y-2 text-sm">
        <dt className="text-neutral-500">Name</dt>
        <dd className="text-neutral-100">
          {!editing && (
            <div className="flex items-center gap-3">
              <span data-testid="me-name">{user.name || "—"}</span>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setValue(user.name ?? "");
                  setEditing(true);
                }}
                aria-label="Edit profile name"
              >
                Edit
              </Button>
            </div>
          )}
          {editing && (
            <form onSubmit={submit} className="flex items-center gap-2">
              <Input
                id="me-name-input"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                disabled={pending}
                autoFocus
                maxLength={100}
              />
              <Button type="submit" size="sm" disabled={pending}>
                {pending ? "Saving…" : "Save"}
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => {
                  setEditing(false);
                  setErr(null);
                }}
                disabled={pending}
              >
                Cancel
              </Button>
            </form>
          )}
          {err && <p className="mt-1 text-xs text-red-400">{err}</p>}
        </dd>
        <dt className="text-neutral-500">Email</dt>
        <dd className="text-neutral-100">{user.email}</dd>
        <dt className="text-neutral-500">User id</dt>
        <dd className="font-mono text-xs text-neutral-300 break-all">
          {user.id}
        </dd>
      </dl>
    </div>
  );
}

// DangerZone exposes the irreversible delete_account action. The backend
// gates with 409 last_admin / team_creator — surface those clearly so the
// operator knows exactly what to clean up first.
function DangerZone({ user }: { user: User }) {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [err, setErr] = useState<{ code?: string; msg: string } | null>(null);
  const [confirmEmail, setConfirmEmail] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr(null);
    setPending(true);
    try {
      await api.deleteAccount();
      clearAuth();
      router.replace("/login");
    } catch (e) {
      if (e instanceof ApiError) {
        setErr({ code: e.code, msg: e.message });
      } else {
        setErr({ msg: "Could not delete account" });
      }
    } finally {
      setPending(false);
    }
  };

  return (
    <Card className="border-red-500/30 bg-red-500/[0.04]">
      <CardHeader className="border-red-500/20">
        <CardTitle className="text-red-200">Delete account</CardTitle>
        <CardDescription className="text-red-300/70">
          Irrevocably remove your Synapse account and any team memberships
          you hold. Refused if you are the last admin of any team or the
          creator of an existing team — clean those up first.
        </CardDescription>
      </CardHeader>
      <CardBody className="flex items-center justify-between gap-3">
        <p className="text-xs text-red-300/70">
          You&apos;ll be signed out and redirected to /login on success.
        </p>
        <Button
          variant="danger"
          onClick={() => {
            setOpen(true);
            setErr(null);
            setConfirmEmail("");
          }}
          aria-label="Delete account"
        >
          Delete account
        </Button>
      </CardBody>

      <Dialog
        open={open}
        onClose={() => !pending && setOpen(false)}
        title="Delete account"
      >
        <form onSubmit={submit} className="space-y-4">
          <p className="text-sm text-neutral-300">
            This permanently removes your account. To confirm, type your
            email below.
          </p>
          <Input
            id="me-delete-confirm"
            value={confirmEmail}
            onChange={(e) => setConfirmEmail(e.target.value)}
            placeholder={user.email}
            autoFocus
          />
          {err && (
            <div className="rounded border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">
              {err.code === "last_admin" && (
                <p>
                  You are the last admin of one or more teams. Promote
                  another admin in each, or delete those teams, then try
                  again.
                </p>
              )}
              {err.code === "team_creator" && (
                <p>
                  You created one or more teams. Delete them first via
                  Team Settings → General → Delete team.
                </p>
              )}
              {err.code !== "last_admin" && err.code !== "team_creator" && (
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
              disabled={pending || confirmEmail !== user.email}
            >
              {pending ? "Deleting…" : "Delete account"}
            </Button>
          </div>
        </form>
      </Dialog>
    </Card>
  );
}
