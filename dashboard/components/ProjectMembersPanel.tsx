"use client";

// ProjectMembersPanel — v1.0+ RBAC UI surface inside the project page.
//
// Lists every member of the owning team merged with their project-level
// override (if any). Project admins can promote/demote per-project,
// add a member with a specific role, or remove an override (which
// drops the user back to their team_members role).
//
// Why this lives on the project page, not a separate /settings/members:
//   - Project membership is project-scoped; a sub-route would mean two
//     levels of nav (team settings + project settings) with one item
//     each. Inline panel keeps the IA flat.
//   - The team-level Members pane already exists and stays distinct —
//     team admins go there to manage *team* membership; project admins
//     come here to manage *override* membership.

import { useState } from "react";
import useSWR from "swr";
import { Avatar } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError, api, type ProjectMember } from "@/lib/api";
import { getCurrentUser } from "@/lib/auth";

const ROLES: ProjectMember["role"][] = ["admin", "member", "viewer"];

const ROLE_TONE: Record<ProjectMember["role"], "neutral" | "green" | "yellow"> = {
  admin: "green",
  member: "neutral",
  viewer: "yellow",
};

export function ProjectMembersPanel({
  projectId,
}: {
  projectId: string;
}) {
  const me = getCurrentUser();
  const { data, error, isLoading, mutate } = useSWR<ProjectMember[]>(
    ["/project-members", projectId],
    () => api.projects.listMembers(projectId),
  );
  // Derive caller role from the merged listing — saves a second API
  // call and keeps the source of truth in one place. If me isn't in
  // the list (shouldn't happen — caller is always a member), assume
  // viewer to avoid showing admin controls by accident.
  const myRole: ProjectMember["role"] =
    data?.find((m) => m.id === me?.id)?.role ?? "viewer";
  const isAdmin = myRole === "admin";
  const [actionErr, setActionErr] = useState<string | null>(null);
  const [pendingId, setPendingId] = useState<string | null>(null);

  const setRole = async (m: ProjectMember, role: ProjectMember["role"]) => {
    if (m.role === role) return;
    setActionErr(null);
    setPendingId(m.id);
    try {
      await api.projects.updateMemberRole(projectId, m.id, role);
      await mutate();
    } catch (e) {
      setActionErr(e instanceof ApiError ? e.message : "Could not update role");
    } finally {
      setPendingId(null);
    }
  };

  const removeOverride = async (m: ProjectMember) => {
    const self = me?.id === m.id;
    if (
      !confirm(
        self
          ? `Drop your project-level override? You'll fall back to your team role.`
          : `Drop ${m.email}'s project override? They'll fall back to their team role.`,
      )
    ) {
      return;
    }
    setActionErr(null);
    setPendingId(m.id);
    try {
      await api.projects.removeMember(projectId, m.id);
      await mutate();
    } catch (e) {
      setActionErr(
        e instanceof ApiError ? e.message : "Could not remove override",
      );
    } finally {
      setPendingId(null);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>Members</CardTitle>
        <CardDescription>
          Everyone with access to this project. Project-level role overrides
          team role for the row marked <code className="font-mono">project</code>.
          {isAdmin
            ? " Set a per-project role to lock a teammate down to read-only, or promote them to admin."
            : " Only project admins can change roles."}
        </CardDescription>
      </CardHeader>
      <CardBody className="p-0">
        {isLoading && (
          <ul className="divide-y divide-neutral-800">
            {[0, 1, 2].map((i) => (
              <li key={i} className="flex items-center gap-3 px-5 py-3">
                <Skeleton className="h-8 w-8 rounded-full" />
                <div className="flex-1">
                  <Skeleton className="h-3.5 w-1/4" />
                  <Skeleton className="mt-2 h-3 w-1/3" />
                </div>
              </li>
            ))}
          </ul>
        )}
        {error && (
          <p className="px-5 py-4 text-xs text-red-400">
            Failed to load members: {(error as Error).message}
          </p>
        )}
        {actionErr && (
          <p className="border-b border-red-500/30 bg-red-500/10 px-5 py-3 text-xs text-red-300">
            {actionErr}
          </p>
        )}
        {data && data.length > 0 && (
          <ul className="divide-y divide-neutral-800">
            {data.map((m) => {
              const self = me?.id === m.id;
              return (
                <li
                  key={m.id}
                  className="flex flex-wrap items-center gap-3 px-5 py-3"
                  data-testid={`project-member-row-${m.email}`}
                >
                  <Avatar seed={m.email} label={m.name || m.email} size="md" />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm text-neutral-100">
                      {m.name || m.email.split("@")[0]}
                      {self && (
                        <span className="ml-2 text-xs text-neutral-500">
                          (you)
                        </span>
                      )}
                    </p>
                    <p className="truncate text-xs text-neutral-500">
                      {m.email}
                    </p>
                  </div>
                  <Badge tone={ROLE_TONE[m.role]}>{m.role}</Badge>
                  <Badge tone="neutral">{m.source}</Badge>
                  {isAdmin && (
                    <div className="flex items-center gap-1">
                      <select
                        value={m.role}
                        onChange={(e) =>
                          setRole(m, e.target.value as ProjectMember["role"])
                        }
                        disabled={pendingId === m.id}
                        className="h-8 rounded-md border border-neutral-700 bg-neutral-900 px-2 text-xs text-neutral-100 focus:border-neutral-500 focus:outline-none"
                        data-testid={`project-member-role-${m.email}`}
                      >
                        {ROLES.map((r) => (
                          <option key={r} value={r}>
                            {r}
                          </option>
                        ))}
                      </select>
                      {m.source === "project" && (
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => removeOverride(m)}
                          disabled={pendingId === m.id}
                          aria-label={`Remove project override for ${m.email}`}
                          data-testid={`project-member-remove-${m.email}`}
                        >
                          Drop override
                        </Button>
                      )}
                    </div>
                  )}
                  {!isAdmin && self && m.source === "project" && (
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => removeOverride(m)}
                      disabled={pendingId === m.id}
                    >
                      Drop my override
                    </Button>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </CardBody>
    </Card>
  );
}
