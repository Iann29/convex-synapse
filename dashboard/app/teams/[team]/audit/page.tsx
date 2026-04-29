"use client";

import Link from "next/link";
import { use } from "react";
import useSWR from "swr";
import { Card, CardBody } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError, api, type AuditEvent, type Team } from "@/lib/api";

type Params = { team: string };

// formatTime keeps time-zone choice client-side; the API returns ISO-8601 in
// UTC. We render in the user's locale to match the rest of the dashboard.
function formatTime(iso: string): string {
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return iso;
  }
}

// describeTarget folds the (targetType, targetId) pair into a single human
// label for the table cell. Audit metadata may also carry a friendlier name
// (e.g. "name" for projects) — prefer that when present.
function describeTarget(e: AuditEvent): string {
  const meta = e.metadata ?? {};
  const friendly =
    typeof meta.name === "string"
      ? meta.name
      : typeof meta.email === "string"
        ? meta.email
        : null;
  if (e.targetType && friendly) return `${e.targetType}: ${friendly}`;
  if (e.targetType && e.targetId) return `${e.targetType}: ${e.targetId.slice(0, 8)}…`;
  if (e.targetType) return e.targetType;
  return "—";
}

export default function AuditLogPage({ params }: { params: Promise<Params> }) {
  const { team: teamRef } = use(params);

  const { data: team } = useSWR<Team>(["/team", teamRef], () =>
    api.teams.get(teamRef),
  );

  // Polling at 30s mirrors the usual "I just ran an action and want to see it
  // appear" feedback loop without slamming the API. Audit log is admin-only,
  // so a 403 from a non-admin viewer surfaces as an error message.
  const { data, error, isLoading } = useSWR(
    ["/audit", teamRef],
    () => api.teams.auditLog(teamRef),
    { refreshInterval: 30_000 },
  );

  const items: AuditEvent[] = data?.items ?? [];

  return (
    <div className="space-y-6">
      <div>
        <nav className="text-xs text-neutral-500">
          <Link href="/teams" className="hover:text-neutral-300">
            Teams
          </Link>{" "}
          /{" "}
          <Link
            href={`/teams/${encodeURIComponent(teamRef)}`}
            className="hover:text-neutral-300"
          >
            {team?.name ?? teamRef}
          </Link>{" "}
          / <span className="text-neutral-300">Audit log</span>
        </nav>
        <div className="mt-3">
          <h1 className="text-xl font-semibold">Audit log</h1>
          <p className="text-xs text-neutral-400">
            Recent actions taken by team members. Updates every 30 seconds.
          </p>
        </div>
      </div>

      {isLoading && (
        <Card>
          <CardBody className="space-y-2">
            <Skeleton className="h-4 w-1/2" />
            <Skeleton className="h-4 w-2/3" />
            <Skeleton className="h-4 w-1/3" />
          </CardBody>
        </Card>
      )}

      {error instanceof ApiError && error.status === 403 && (
        <Card>
          <CardBody>
            <p className="text-sm text-neutral-300">
              Only team admins can view the audit log.
            </p>
          </CardBody>
        </Card>
      )}

      {error && !(error instanceof ApiError && error.status === 403) && (
        <p className="text-sm text-red-400">
          Failed to load audit log: {(error as Error).message}
        </p>
      )}

      {data && items.length === 0 && (
        <Card>
          <CardBody className="text-center">
            <p className="text-sm text-neutral-300">
              No audit events recorded yet.
            </p>
            <p className="mt-1 text-xs text-neutral-500">
              Events appear here when members create projects, deployments, or
              invite teammates.
            </p>
          </CardBody>
        </Card>
      )}

      {data && items.length > 0 && (
        <Card>
          <CardBody className="p-0">
            <table
              className="w-full text-left text-sm"
              data-testid="audit-log-table"
            >
              <thead>
                <tr className="border-b border-neutral-800 text-xs uppercase tracking-wide text-neutral-500">
                  <th className="px-4 py-3 font-medium">Action</th>
                  <th className="px-4 py-3 font-medium">Actor</th>
                  <th className="px-4 py-3 font-medium">Target</th>
                  <th className="px-4 py-3 font-medium">When</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-neutral-800">
                {items.map((e) => (
                  <tr key={e.id} data-testid="audit-log-row">
                    <td className="px-4 py-3 font-mono text-xs text-neutral-100">
                      {e.action}
                    </td>
                    <td className="px-4 py-3 text-xs text-neutral-300">
                      {e.actorEmail ?? e.actorId ?? "—"}
                    </td>
                    <td className="px-4 py-3 text-xs text-neutral-400">
                      {describeTarget(e)}
                    </td>
                    <td className="px-4 py-3 text-xs text-neutral-500">
                      {formatTime(e.createTime)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </CardBody>
        </Card>
      )}
    </div>
  );
}
