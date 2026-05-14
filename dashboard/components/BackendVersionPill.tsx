"use client";

import useSWR from "swr";
import clsx from "clsx";
import { ApiError, api, type BackendVersion } from "@/lib/api";

// Small per-deployment pill that shows what version of the Convex
// backend image the container is currently running and when it was
// last (re)created. Sits inside each deployment card on the project
// page so operators stop having to docker-inspect or grep through
// upgrade logs to answer "is this deployment stale?".
//
// Cache contract: the backend endpoint memoises probe results for
// ~60s, and the dashboard polls every 5min on focus — operators
// get fresh data after they docker-recreate without us hammering
// /version every render.
//
// Fails gracefully: probe errors land in `data.error` and we render a
// muted "—" instead of a broken "couldn't load" pill. Adopted
// deployments report error="adopted_deployment" since we can't reach
// the operator's external backend over our docker network.
export function BackendVersionPill({
  deploymentName,
}: {
  deploymentName: string;
}) {
  const { data, error } = useSWR<BackendVersion>(
    deploymentName ? `/backend_version:${deploymentName}` : null,
    () => api.deployments.backendVersion(deploymentName),
    {
      refreshInterval: 5 * 60 * 1000,
      revalidateOnFocus: true,
      shouldRetryOnError: false,
    },
  );

  // Don't render anything for unauth — same pattern as VersionStatusChip.
  if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
    return null;
  }

  const probeFailed = !!data?.error || (!data?.version && !data);
  const version = data?.version?.trim() || "—";
  const ageLabel = relativeAge(data?.lastDeployAt);
  const tooltip = buildTooltip(data);

  return (
    <span
      title={tooltip}
      className={clsx(
        "inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 font-mono text-xs",
        probeFailed
          ? "border-neutral-800 bg-neutral-950/60 text-neutral-500"
          : "border-emerald-900/80 bg-emerald-950/30 text-emerald-300",
      )}
    >
      <span aria-hidden className={probeFailed ? "text-neutral-600" : "text-emerald-500"}>
        ●
      </span>
      <span>convex {version}</span>
      {ageLabel && !probeFailed ? (
        <span className="text-neutral-500">· {ageLabel}</span>
      ) : null}
    </span>
  );
}

// relativeAge picks the coarsest unit that's >1 to keep the chip short:
// "2h ago", "3d ago", "1mo ago". Returns "" when the timestamp is
// missing so the caller can drop the trailing dot separator.
function relativeAge(iso?: string): string {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "";
  const seconds = Math.max(0, (Date.now() - t) / 1000);
  if (seconds < 60) return "just now";
  const minutes = seconds / 60;
  if (minutes < 60) return `${Math.floor(minutes)}m ago`;
  const hours = minutes / 60;
  if (hours < 24) return `${Math.floor(hours)}h ago`;
  const days = hours / 24;
  if (days < 30) return `${Math.floor(days)}d ago`;
  const months = days / 30;
  if (months < 12) return `${Math.floor(months)}mo ago`;
  return `${Math.floor(days / 365)}y ago`;
}

function buildTooltip(data?: BackendVersion): string {
  if (!data) return "Loading backend version...";
  const parts: string[] = [];
  if (data.version) parts.push(`Backend version: ${data.version}`);
  if (data.lastDeployAt) parts.push(`Last (re)deploy: ${new Date(data.lastDeployAt).toLocaleString()}`);
  if (data.fetchedAt) parts.push(`Checked: ${new Date(data.fetchedAt).toLocaleString()}`);
  if (data.fromCache) parts.push("(cached)");
  if (data.error) parts.push(`Probe failed: ${data.error}`);
  return parts.join("\n");
}
