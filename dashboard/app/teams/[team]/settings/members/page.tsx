"use client";

import { use } from "react";
import useSWR from "swr";
import { Avatar } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { InvitesPanel } from "@/components/InvitesPanel";
import { ApiError, api, type TeamMember } from "@/lib/api";

type Params = { team: string };

// Members pane. Renders the live roster from /list_members and the same
// InvitesPanel that ships on the team home (the e2e suite still issues
// invites from the home page; this is the new IA-correct location).
export default function TeamMembersPage({
  params,
}: {
  params: Promise<Params>;
}) {
  const { team: teamRef } = use(params);
  const { data, error, isLoading } = useSWR<TeamMember[]>(
    ["/members", teamRef],
    () => api.teams.listMembers(teamRef),
  );

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle>Members</CardTitle>
          <CardDescription>
            Everyone with access to this team's projects and deployments.
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

          {error && !(error instanceof ApiError && error.status === 403) && (
            <p className="px-5 py-4 text-xs text-red-400">
              Failed to load members: {(error as Error).message}
            </p>
          )}

          {data && data.length > 0 && (
            <ul className="divide-y divide-neutral-800">
              {data.map((m) => (
                <li
                  key={m.id}
                  className="flex items-center gap-3 px-5 py-3"
                >
                  <Avatar seed={m.email} label={m.name || m.email} size="md" />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm text-neutral-100">
                      {m.name || m.email.split("@")[0]}
                    </p>
                    <p className="truncate text-xs text-neutral-500">
                      {m.email}
                    </p>
                  </div>
                  <Badge tone={m.role === "admin" ? "neutral" : "neutral"}>
                    {m.role}
                  </Badge>
                </li>
              ))}
            </ul>
          )}

          {data && data.length === 0 && (
            <p className="px-5 py-8 text-center text-sm text-neutral-500">
              No members visible.
            </p>
          )}
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Invite a teammate</CardTitle>
          <CardDescription>
            Send an invite link to add someone to this team.
          </CardDescription>
        </CardHeader>
        <CardBody>
          <InvitesPanel teamRef={teamRef} />
        </CardBody>
      </Card>
    </div>
  );
}
