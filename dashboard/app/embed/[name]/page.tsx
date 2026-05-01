"use client";

import { use, useEffect, useRef, useState } from "react";
import { ApiError, api, type DeploymentAuth } from "@/lib/api";

// Where the open-source Convex Dashboard is hosted. Synapse runs it
// as the `convex-dashboard` service in docker-compose; setup.sh wires
// this env var to http://<vps-ip>:6791 (or https://<domain>:6791).
const CONVEX_DASHBOARD_URL =
  process.env.NEXT_PUBLIC_CONVEX_DASHBOARD_URL?.replace(/\/$/, "") ||
  "http://localhost:6791";

// Origin used for the postMessage handshake — derived from the URL
// above. Restricting the target origin prevents creds from leaking
// to a different page if the operator misconfigures the var.
const CONVEX_DASHBOARD_ORIGIN = (() => {
  try {
    return new URL(CONVEX_DASHBOARD_URL).origin;
  } catch {
    return "*";
  }
})();

type Params = { name: string };

/**
 * /embed/<deployment-name> — bridge page that opens the open-source
 * Convex Dashboard inside an iframe and answers its postMessage
 * handshake with this deployment's adminKey + deploymentUrl, so the
 * operator lands on the data/functions/logs UI without having to
 * paste credentials manually.
 *
 * The handshake protocol is documented in Convex's
 * `npm-packages/dashboard-self-hosted/src/pages/_app.tsx` (see
 * `useEmbeddedDashboardCredentials`):
 *
 *     iframe -> parent: { type: "dashboard-credentials-request" }
 *     parent -> iframe: { type: "dashboard-credentials",
 *                          adminKey, deploymentUrl, deploymentName,
 *                          visiblePages? }
 *
 * Synapse fork's earlier "open in new tab with hash" approach didn't
 * work — the self-hosted dashboard build never parses
 * `window.location.hash` (only its `?a=&d=` query params, which are
 * for the local-CLI list flow, not auth).
 */
export default function EmbedDashboardPage({
  params,
}: {
  params: Promise<Params>;
}) {
  const { name } = use(params);
  const [auth, setAuth] = useState<DeploymentAuth | null>(null);
  const [error, setError] = useState<string | null>(null);
  const iframeRef = useRef<HTMLIFrameElement | null>(null);

  // Fetch creds once on mount.
  useEffect(() => {
    let cancelled = false;
    api.deployments
      .auth(name)
      .then((d) => {
        if (!cancelled) setAuth(d);
      })
      .catch((err) => {
        if (!cancelled) {
          setError(
            err instanceof ApiError
              ? err.message
              : "Could not load deployment credentials",
          );
        }
      });
    return () => {
      cancelled = true;
    };
  }, [name]);

  // Reply to the dashboard's handshake. The iframe sends the request
  // on mount, possibly multiple times until it hears back; we stay
  // subscribed for the lifetime of the page.
  useEffect(() => {
    if (!auth) return;
    function handleMessage(event: MessageEvent) {
      // Origin check — only respond to messages coming from the
      // dashboard iframe itself. Falls back to "*" only when the
      // configured URL was unparseable (set above).
      if (
        CONVEX_DASHBOARD_ORIGIN !== "*" &&
        event.origin !== CONVEX_DASHBOARD_ORIGIN
      ) {
        return;
      }
      const data = event.data as { type?: string } | null;
      if (data?.type !== "dashboard-credentials-request") return;
      const target = iframeRef.current?.contentWindow;
      if (!target) return;
      target.postMessage(
        {
          type: "dashboard-credentials",
          adminKey: auth!.adminKey,
          deploymentUrl: auth!.deploymentUrl,
          deploymentName: auth!.deploymentName,
        },
        CONVEX_DASHBOARD_ORIGIN,
      );
    }
    window.addEventListener("message", handleMessage);
    return () => window.removeEventListener("message", handleMessage);
  }, [auth]);

  if (error) {
    return (
      <div className="flex min-h-screen items-center justify-center p-8">
        <div className="max-w-md text-center text-sm text-red-500">
          <p className="font-semibold">Failed to open dashboard</p>
          <p className="mt-2">{error}</p>
          <p className="mt-4 text-xs text-zinc-400">
            Deployment: <code>{name}</code>
          </p>
        </div>
      </div>
    );
  }

  if (!auth) {
    return (
      <div className="flex min-h-screen items-center justify-center p-8 text-sm text-zinc-400">
        Loading deployment credentials for <code className="ml-1">{name}</code>…
      </div>
    );
  }

  return (
    <iframe
      ref={iframeRef}
      src={CONVEX_DASHBOARD_URL}
      title={`${name} — Convex Dashboard`}
      className="h-screen w-full border-0"
      // The dashboard makes XHR calls to the deployment URL; allow
      // same-origin (within the iframe) plus scripts (it's a SPA).
      sandbox="allow-same-origin allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox"
    />
  );
}
