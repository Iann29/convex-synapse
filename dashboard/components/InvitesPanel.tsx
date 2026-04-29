"use client";

import { useState } from "react";
import useSWR from "swr";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { ApiError, api, type PendingInvite } from "@/lib/api";

type Props = { teamRef: string };

// Invite admins use this panel to send invites and revoke open ones.
// Tokens are returned ONCE (after invite creation) and remain visible for as
// long as the invite is pending — by design, since admins are the trust
// anchor here. Anyone with a token can join the team.
export function InvitesPanel({ teamRef }: Props) {
  const { data, error, mutate } = useSWR<PendingInvite[]>(
    ["/invites", teamRef],
    () => api.teams.listInvites(teamRef),
  );

  const [email, setEmail] = useState("");
  const [role, setRole] = useState<"member" | "admin">("member");
  const [pending, setPending] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [lastIssued, setLastIssued] = useState<{ email: string; token: string } | null>(null);

  const sendInvite = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    if (!email.trim()) return;
    setPending(true);
    try {
      const res = await api.teams.invite(teamRef, email.trim(), role);
      setLastIssued({ email: res.email, token: res.inviteToken });
      setEmail("");
      await mutate();
    } catch (err) {
      // 403 is the typical case (non-admin trying to invite). Surface it
      // gently rather than blowing up the panel.
      setFormError(err instanceof ApiError ? err.message : "Could not invite");
    } finally {
      setPending(false);
    }
  };

  const cancel = async (id: string) => {
    setFormError(null);
    try {
      await api.teams.cancelInvite(teamRef, id);
      await mutate();
    } catch (err) {
      setFormError(
        err instanceof ApiError ? err.message : "Could not cancel invite",
      );
    }
  };

  // 403 from the list call → caller is not an admin. Render nothing —
  // members shouldn't see the section at all.
  if (error instanceof ApiError && error.status === 403) {
    return null;
  }

  return (
    <section className="space-y-3">
      <div>
        <h2 className="text-sm font-semibold text-neutral-200">Invites</h2>
        <p className="text-xs text-neutral-500">
          Pending invitations to this team.
        </p>
      </div>

      {error && !(error instanceof ApiError && error.status === 403) && (
        <p className="text-xs text-red-400">
          Failed to load invites: {(error as Error).message}
        </p>
      )}

      {data && data.length > 0 && (
        <Card>
          <CardBody className="divide-y divide-neutral-800 p-0">
            {data.map((inv) => (
              <div
                key={inv.id}
                className="flex items-center justify-between gap-3 px-4 py-3 text-sm"
              >
                <div className="min-w-0 flex-1">
                  <p className="truncate text-neutral-100">{inv.email}</p>
                  <p className="font-mono text-xs text-neutral-500 truncate">
                    role: {inv.role} · token: {inv.token}
                  </p>
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => cancel(inv.id)}
                  aria-label={`Cancel invite for ${inv.email}`}
                >
                  Cancel
                </Button>
              </div>
            ))}
          </CardBody>
        </Card>
      )}

      {data && data.length === 0 && (
        <p className="text-xs text-neutral-500">No pending invites.</p>
      )}

      <form onSubmit={sendInvite} className="flex flex-wrap items-end gap-2">
        <div className="flex-1 min-w-[14rem] space-y-1">
          <label htmlFor="invite-email" className="block text-xs text-neutral-400">
            Email
          </label>
          <Input
            id="invite-email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="teammate@example.com"
            required
          />
        </div>
        <div className="space-y-1">
          <label htmlFor="invite-role" className="block text-xs text-neutral-400">
            Role
          </label>
          <select
            id="invite-role"
            className="h-9 rounded-md border border-neutral-700 bg-neutral-900 px-3 text-sm text-neutral-100"
            value={role}
            onChange={(e) => setRole(e.target.value as "member" | "admin")}
          >
            <option value="member">member</option>
            <option value="admin">admin</option>
          </select>
        </div>
        <Button type="submit" disabled={pending || !email.trim()}>
          {pending ? "Inviting…" : "Invite"}
        </Button>
      </form>

      {formError && <p className="text-xs text-red-400">{formError}</p>}

      {lastIssued && (
        <Card>
          <CardBody>
            <p className="text-xs text-neutral-300">
              Invite issued for <span className="font-medium">{lastIssued.email}</span>.
              Share this URL with them:
            </p>
            <p className="mt-2 break-all rounded bg-neutral-900 px-3 py-2 font-mono text-xs text-neutral-100">
              {typeof window !== "undefined" ? window.location.origin : ""}/accept-invite?token=
              {lastIssued.token}
            </p>
          </CardBody>
        </Card>
      )}
    </section>
  );
}
