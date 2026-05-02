"use client";

import { useEffect, useState } from "react";
import useSWR from "swr";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import {
  ApiError,
  api,
  type UpgradeStatus,
  type VersionCheck,
} from "@/lib/api";

// UpdateBanner pokes /v1/admin/version_check once an hour and surfaces
// "vX.Y.Z available" when GitHub's /releases/latest beats the running
// version. Clicking opens a dialog with release notes + a button that
// kicks off the host-side updater daemon. Persisted "I dismissed
// vX.Y.Z" keeps the banner from re-appearing for the same version
// after a manual dismiss; a fresh release re-arms.
//
// The whole thing fails-soft: if the user lacks team-admin scope,
// /version_check returns 403 and we render nothing — no point showing
// admins-only UI to a viewer.
//
// During the upgrade itself, the synapse-api container restarts; the
// dialog detects the connection drop, flips into a "page will reload
// in 90s" mode, and forces a reload so the new version's banner
// component takes over.
export function UpdateBanner() {
  const { data, error } = useSWR<VersionCheck>(
    "/v1/admin/version_check",
    () => api.admin.versionCheck(),
    {
      refreshInterval: 60 * 60 * 1000, // 1h — caps GitHub fetches comfortably under rate limit
      revalidateOnFocus: false,
      shouldRetryOnError: false,
    },
  );

  const [open, setOpen] = useState(false);
  const [dismissed, setDismissed] = useState<string | null>(null);

  // localStorage gives us per-browser dismissal across page refreshes
  // without forcing the operator to re-evaluate every load. Once a new
  // version ships, the key changes and the banner reappears.
  useEffect(() => {
    if (typeof window === "undefined") return;
    setDismissed(window.localStorage.getItem("synapse-update-dismissed"));
  }, []);

  // Hide the whole feature for unauth/forbidden — every other case
  // shows SOMETHING (current version + error if GitHub was unreachable).
  if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
    return null;
  }
  if (!data || !data.updateAvailable || !data.latest) return null;
  if (dismissed && dismissed === data.latest) return null;

  const dismiss = () => {
    if (typeof window !== "undefined" && data.latest) {
      window.localStorage.setItem("synapse-update-dismissed", data.latest);
      setDismissed(data.latest);
    }
  };

  return (
    <>
      <div className="border-b border-amber-900/60 bg-amber-950/40">
        <div className="mx-auto flex max-w-7xl flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2 text-xs sm:px-6">
          <span aria-hidden className="text-amber-300">
            ●
          </span>
          <span className="text-amber-100">
            <span className="font-semibold">Synapse v{data.latest}</span> is
            available — you&apos;re on v{data.current}.
          </span>
          <button
            type="button"
            onClick={() => setOpen(true)}
            className="rounded-md bg-amber-200/10 px-2 py-1 font-medium text-amber-100 transition hover:bg-amber-200/20"
          >
            Review &amp; upgrade
          </button>
          <button
            type="button"
            onClick={dismiss}
            className="ml-auto text-amber-300/70 transition hover:text-amber-200"
            aria-label="Dismiss this update notification"
          >
            Dismiss
          </button>
        </div>
      </div>
      <UpgradeDialog
        open={open}
        onClose={() => setOpen(false)}
        check={data}
      />
    </>
  );
}

// UpgradeDialog walks the operator through release notes → confirm →
// progress polling. The "page will reload" handoff at the end is
// because the synapse-api container is recreated mid-upgrade — at
// that point we lose the ability to fetch /status, so we set a
// timeout and reload. After reload the new version stamps
// `current=newVersion` and the banner stays hidden.
type DialogState =
  | "review"
  | "confirming"
  | "starting"
  | "polling"
  | "rebooting"
  | "succeeded"
  | "failed";

function UpgradeDialog({
  open,
  onClose,
  check,
}: {
  open: boolean;
  onClose: () => void;
  check: VersionCheck;
}) {
  const [state, setState] = useState<DialogState>("review");
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<UpgradeStatus | null>(null);

  // Reset every time the modal reopens — operator may have already
  // run an upgrade in this same browser session.
  useEffect(() => {
    if (open) {
      setState("review");
      setError(null);
      setStatus(null);
    }
  }, [open]);

  // Poll /status while running. We poll aggressively (2.5s) — the
  // updater itself is local, the bandwidth is nothing, and the log
  // tail is what the operator stares at to know it's alive. Stops
  // when state moves out of `polling`.
  useEffect(() => {
    if (state !== "polling") return;
    let cancelled = false;
    let consecutiveFailures = 0;

    const tick = async () => {
      try {
        const s = await api.admin.upgradeStatus();
        if (cancelled) return;
        consecutiveFailures = 0;
        setStatus(s);
        if (s.state === "success") setState("succeeded");
        else if (s.state === "failed") setState("failed");
      } catch {
        if (cancelled) return;
        consecutiveFailures++;
        // synapse-api restarts mid-upgrade. The poll fails. Once we
        // see 3 consecutive failures we assume the API is in the
        // restart window and shift to the "page will reload" state —
        // the upgrade likely succeeded; the operator's session just
        // can't see /status anymore. Forcing a reload after 90s
        // brings the operator back in on the new build.
        if (consecutiveFailures >= 3) {
          setState("rebooting");
        }
      }
    };

    void tick();
    const id = window.setInterval(tick, 2500);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
  }, [state]);

  // Auto-reload after entering the "rebooting" state. 90s is the
  // upper bound for compose's `up -d --build` of a small Synapse
  // stack on a Hetzner CPX22; faster boxes are back well before then.
  useEffect(() => {
    if (state !== "rebooting") return;
    const id = window.setTimeout(() => window.location.reload(), 90_000);
    return () => window.clearTimeout(id);
  }, [state]);

  const startUpgrade = async () => {
    setError(null);
    setState("starting");
    try {
      await api.admin.upgrade();
      setState("polling");
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : "Could not start the upgrade",
      );
      setState("review");
    }
  };

  const dialogTitle = (() => {
    switch (state) {
      case "review":
      case "confirming":
        return `Upgrade to Synapse v${check.latest}`;
      case "starting":
      case "polling":
        return "Upgrading Synapse…";
      case "rebooting":
        return "Almost there — reloading the page";
      case "succeeded":
        return "Upgrade complete";
      case "failed":
        return "Upgrade failed";
    }
  })();

  return (
    <Dialog open={open} onClose={onClose} title={dialogTitle}>
      {state === "review" && (
        <div className="space-y-3">
          <p className="text-xs text-neutral-400">
            Currently on{" "}
            <code className="rounded bg-neutral-800 px-1 py-0.5 font-mono text-[11px] text-neutral-100">
              v{check.current}
            </code>
            , latest is{" "}
            <code className="rounded bg-neutral-800 px-1 py-0.5 font-mono text-[11px] text-neutral-100">
              v{check.latest}
            </code>
            . The upgrade runs{" "}
            <code className="rounded bg-neutral-800 px-1 py-0.5 font-mono text-[11px] text-neutral-100">
              setup.sh --upgrade
            </code>{" "}
            on your VPS via the host-side daemon.
          </p>

          {check.releaseNotes && (
            <div className="space-y-1.5">
              <p className="text-xs font-semibold text-neutral-200">
                Release notes
              </p>
              <pre className="max-h-72 overflow-auto whitespace-pre-wrap rounded-md border border-neutral-800/80 bg-neutral-950 p-3 font-mono text-[11px] leading-relaxed text-neutral-300">
                {check.releaseNotes}
              </pre>
            </div>
          )}

          {check.releaseUrl && (
            <p className="text-xs">
              <a
                href={check.releaseUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="text-amber-200 hover:underline"
              >
                View on GitHub →
              </a>
            </p>
          )}

          <p className="rounded bg-amber-900/30 px-3 py-2 text-xs text-amber-200">
            <span className="font-semibold">Heads up:</span> the dashboard will
            briefly go offline while containers restart. This window will
            keep polling status; if it loses contact for ~10s it&apos;ll
            auto-reload after the upgrade window completes.
          </p>

          {error && <p className="text-xs text-red-400">{error}</p>}

          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button onClick={() => setState("confirming")}>Continue</Button>
          </div>
        </div>
      )}

      {state === "confirming" && (
        <div className="space-y-3">
          <p className="text-xs text-neutral-300">
            Last check: ready to upgrade from v{check.current} to v
            {check.latest}. This will restart the synapse-api,
            synapse-dashboard and caddy containers. Existing Convex
            deployments keep running uninterrupted (they&apos;re separate
            containers Synapse manages, not part of the upgrade).
          </p>
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setState("review")}>
              Back
            </Button>
            <Button onClick={startUpgrade}>Upgrade now</Button>
          </div>
        </div>
      )}

      {(state === "starting" ||
        state === "polling" ||
        state === "rebooting") && (
        <div className="space-y-3">
          <div className="flex items-center gap-2 text-xs text-neutral-300">
            <Spinner />
            <span>
              {state === "starting" && "Asking the updater to start…"}
              {state === "polling" &&
                (status?.state === "running"
                  ? `Running (started ${formatTime(status.startedAt)})`
                  : "Waiting for updater to start setup.sh…")}
              {state === "rebooting" &&
                "Synapse API is restarting; the page will reload automatically (~90s)."}
            </span>
          </div>
          {status?.logTail && status.logTail.length > 0 && (
            <pre className="max-h-72 overflow-auto whitespace-pre-wrap rounded-md border border-neutral-800/80 bg-neutral-950 p-3 font-mono text-[11px] leading-snug text-neutral-300">
              {status.logTail.slice(-50).join("\n")}
            </pre>
          )}
          {state === "rebooting" && (
            <Button onClick={() => window.location.reload()}>
              Reload now
            </Button>
          )}
        </div>
      )}

      {state === "succeeded" && (
        <div className="space-y-3">
          <p className="rounded bg-emerald-900/40 px-3 py-2 text-xs text-emerald-200">
            <span className="font-semibold">✓ Synapse upgraded.</span> Reload
            the page to pick up the new dashboard build.
          </p>
          {status?.logTail && (
            <pre className="max-h-48 overflow-auto whitespace-pre-wrap rounded-md border border-neutral-800/80 bg-neutral-950 p-3 font-mono text-[11px] text-neutral-400">
              {status.logTail.slice(-20).join("\n")}
            </pre>
          )}
          <div className="flex justify-end gap-2">
            <Button onClick={() => window.location.reload()}>Reload</Button>
          </div>
        </div>
      )}

      {state === "failed" && (
        <div className="space-y-3">
          <p className="rounded bg-red-900/40 px-3 py-2 text-xs text-red-200">
            <span className="font-semibold">Upgrade failed.</span> Synapse
            attempted automatic rollback. SSH to your VPS and run{" "}
            <code className="rounded bg-neutral-900 px-1 py-0.5 font-mono">
              ./setup.sh --doctor
            </code>{" "}
            to confirm the running version, then retry from the dashboard or
            via the CLI.
          </p>
          {status?.logTail && (
            <pre className="max-h-72 overflow-auto whitespace-pre-wrap rounded-md border border-neutral-800/80 bg-neutral-950 p-3 font-mono text-[11px] text-neutral-300">
              {status.logTail.join("\n")}
            </pre>
          )}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>
              Close
            </Button>
            <Button onClick={() => setState("review")}>Try again</Button>
          </div>
        </div>
      )}
    </Dialog>
  );
}

function Spinner() {
  return (
    <svg
      className="h-3 w-3 animate-spin text-amber-300"
      viewBox="0 0 24 24"
      aria-hidden
    >
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

function formatTime(iso?: string): string {
  if (!iso) return "—";
  try {
    return new Date(iso).toLocaleTimeString();
  } catch {
    return iso;
  }
}
