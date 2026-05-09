"use client";

import { useEffect, useRef, useState } from "react";
import useSWR from "swr";
import clsx from "clsx";
import { ApiError, api, type VersionCheck } from "@/lib/api";

// Persistent version pill in the top nav. Three reasons it exists:
//   1. Shows the running version at all times — operators stop guessing
//      "what tag am I on right now."
//   2. Surfaces the GitHub-cache TTL as a live countdown — anxious
//      operators who just published a release see when the dashboard
//      will look at GitHub again, instead of staring at a stale banner.
//   3. "Check now" button bypasses the 15-minute cache (rate-limited
//      server-side at 30s between busts) so the operator doesn't have
//      to wait for the next auto-poll.
//
// Renders nothing for unauth/forbidden users — same pattern as
// UpdateBanner; the version_check endpoint is instance-admin gated.
export function VersionStatusChip() {
  const { data, error, mutate, isValidating } = useSWR<VersionCheck>(
    "/v1/admin/version_check",
    () => api.admin.versionCheck(),
    {
      // 1h auto-refresh. The TTL on the GitHub side is 15min, so
      // hourly is conservative; the manual "Check now" button covers
      // the impatient case.
      refreshInterval: 60 * 60 * 1000,
      revalidateOnFocus: false,
      shouldRetryOnError: false,
    },
  );

  const [open, setOpen] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  // Click-outside / Escape to close the popover.
  useEffect(() => {
    if (!open) return;
    const onClick = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onClick);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onClick);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  // Hide the whole feature for non-admin users (same gate as
  // /version_check). Also hide before the first response lands so we
  // don't flash an empty pill.
  if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
    return null;
  }
  if (!data) return null;

  const updateAvailable = data.updateAvailable && !!data.latest;

  const onCheckNow = async () => {
    if (refreshing) return;
    setRefreshing(true);
    try {
      const fresh = await api.admin.versionCheckRefresh();
      await mutate(fresh, { revalidate: false });
    } catch {
      // Best-effort — surface nothing on error here, the next
      // auto-refresh will retry. The chip stays interactive.
    } finally {
      setRefreshing(false);
    }
  };

  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="dialog"
        aria-expanded={open}
        data-testid="version-status-chip"
        className={clsx(
          "inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-[11px] font-medium transition-colors",
          updateAvailable
            ? "bg-amber-500/15 text-amber-200 hover:bg-amber-500/25"
            : "text-neutral-400 hover:bg-neutral-900 hover:text-neutral-200",
        )}
        title={
          updateAvailable
            ? `v${data.latest} available — click for details`
            : "Up to date — click for cache status"
        }
      >
        <span
          aria-hidden
          className={clsx(
            "h-1.5 w-1.5 rounded-full",
            updateAvailable ? "bg-amber-400" : "bg-emerald-500/70",
          )}
        />
        <span className="font-mono">v{data.current}</span>
        {updateAvailable && (
          <span className="text-amber-300/80">
            → v{data.latest}
          </span>
        )}
      </button>

      {open && (
        <div
          role="dialog"
          aria-label="Update status"
          className="synapse-fade-in absolute right-0 top-full z-50 mt-2 w-72 overflow-hidden rounded-lg border border-neutral-800 bg-[#141416] shadow-2xl"
        >
          <div className="border-b border-neutral-800 px-3 py-2">
            <p className="text-[11px] uppercase tracking-wider text-neutral-500">
              Update status
            </p>
          </div>

          <div className="space-y-2.5 px-3 py-3 text-xs">
            <div className="flex items-center justify-between">
              <span className="text-neutral-500">Current</span>
              <code className="font-mono text-neutral-100">v{data.current}</code>
            </div>

            <div className="flex items-center justify-between">
              <span className="text-neutral-500">Latest</span>
              {data.latest ? (
                <code
                  className={clsx(
                    "font-mono",
                    updateAvailable ? "text-amber-200" : "text-neutral-100",
                  )}
                >
                  v{data.latest}
                </code>
              ) : (
                <span className="text-neutral-500">unknown</span>
              )}
            </div>

            <FetchedAtRow fetchedAt={data.fetchedAt} fromCache={data.fromCache} />
            <NextCheckRow
              cacheExpiresAt={data.cacheExpiresAt}
              fromCache={data.fromCache}
            />

            {data.error && (
              <p className="rounded bg-red-900/30 px-2 py-1.5 text-[11px] text-red-200">
                GitHub unreachable: {data.error}
              </p>
            )}

            <button
              type="button"
              onClick={onCheckNow}
              disabled={refreshing || isValidating}
              data-testid="version-status-check-now"
              className={clsx(
                "mt-1 flex w-full items-center justify-center gap-2 rounded-md border border-neutral-700 px-2 py-1.5 text-[11px] transition-colors",
                refreshing || isValidating
                  ? "cursor-not-allowed bg-neutral-900 text-neutral-500"
                  : "bg-neutral-900 text-neutral-200 hover:bg-neutral-800",
              )}
            >
              {refreshing ? (
                <>
                  <Spinner />
                  <span>Asking GitHub…</span>
                </>
              ) : (
                <span>Check now</span>
              )}
            </button>

            <p className="text-[10px] leading-relaxed text-neutral-500">
              Synapse caches GitHub releases for 15 min to stay under the
              unauthenticated rate limit. &ldquo;Check now&rdquo; busts
              the cache; rate-limited to 30s between manual busts.
            </p>
          </div>
        </div>
      )}
    </div>
  );
}

function FetchedAtRow({
  fetchedAt,
  fromCache,
}: {
  fetchedAt?: string;
  fromCache?: boolean;
}) {
  // Live "checked Xmin ago" — re-renders every 30s so the pill stays
  // honest without spamming setState. Falls back to "—" when GitHub
  // has never been reached.
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 30_000);
    return () => window.clearInterval(id);
  }, []);

  if (!fetchedAt) {
    return (
      <div className="flex items-center justify-between">
        <span className="text-neutral-500">Last check</span>
        <span className="text-neutral-500">—</span>
      </div>
    );
  }
  const ago = formatRelative(now, new Date(fetchedAt).getTime());
  return (
    <div className="flex items-center justify-between">
      <span className="text-neutral-500">Last check</span>
      <span className="text-neutral-300" data-testid="version-status-last-check">
        {ago}
        {fromCache ? "" : " (live)"}
      </span>
    </div>
  );
}

function NextCheckRow({
  cacheExpiresAt,
  fromCache,
}: {
  cacheExpiresAt?: string;
  fromCache?: boolean;
}) {
  // Live MM:SS countdown to next GitHub fetch. Updates once per second
  // while the popover is open; component unmount tears the interval
  // down. When the cache is already past its TTL the next request
  // will refetch — show "ready to refresh".
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, []);

  if (!cacheExpiresAt) {
    return (
      <div className="flex items-center justify-between">
        <span className="text-neutral-500">Next auto-check</span>
        <span className="text-neutral-500">—</span>
      </div>
    );
  }

  const expiresMs = new Date(cacheExpiresAt).getTime();
  const remaining = Math.max(0, expiresMs - now);

  if (remaining === 0) {
    return (
      <div className="flex items-center justify-between">
        <span className="text-neutral-500">Next auto-check</span>
        <span className="text-emerald-300/90">ready to refresh</span>
      </div>
    );
  }

  const totalSec = Math.floor(remaining / 1000);
  const mm = Math.floor(totalSec / 60);
  const ss = totalSec % 60;
  return (
    <div className="flex items-center justify-between">
      <span className="text-neutral-500">
        {fromCache ? "Cache expires" : "Cached for"}
      </span>
      <span
        className="font-mono text-neutral-300"
        data-testid="version-status-countdown"
      >
        {mm}:{ss.toString().padStart(2, "0")}
      </span>
    </div>
  );
}

function formatRelative(now: number, then: number): string {
  const diff = Math.max(0, now - then);
  if (diff < 30_000) return "just now";
  const sec = Math.floor(diff / 1000);
  if (sec < 60) return `${sec}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}min ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.floor(hr / 24);
  return `${day}d ago`;
}

function Spinner() {
  return (
    <svg className="h-3 w-3 animate-spin" viewBox="0 0 24 24" aria-hidden>
      <circle
        cx="12"
        cy="12"
        r="10"
        stroke="currentColor"
        strokeWidth="3"
        fill="none"
        opacity="0.25"
      />
      <path
        d="M12 2a10 10 0 0 1 10 10"
        stroke="currentColor"
        strokeWidth="3"
        fill="none"
      />
    </svg>
  );
}
