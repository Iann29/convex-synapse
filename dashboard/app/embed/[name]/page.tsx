"use client";

import Link from "next/link";
import { use, useEffect, useRef, useState } from "react";
import useSWR from "swr";
import { DeploymentPicker } from "@/components/DeploymentPicker";
import {
  ApiError,
  api,
  type Deployment,
  type DeploymentAuth,
  type Project,
  type Team,
} from "@/lib/api";

// Where the open-source Convex Dashboard is hosted, as seen by the
// browser.
//
// Architecture timeline:
//   v1.x ... v1.6.10: `https://{{DOMAIN}}:6791` (cross-origin to
//     the parent page on :443). Caddy did NOT front :6791 — the
//     port was a plain-HTTP mapping into convex-dashboard-proxy.
//     Under HTTPS parent pages every iframe load died with a
//     Mixed-Content block and the iframe stayed black.
//   v1.6.11: tried to fix by routing the iframe through a
//     same-origin `/__convex/*` chi mount. Killed the cross-origin
//     problem AND introduced a new one: the upstream Convex
//     Dashboard image is a Next.js SPA that emits absolute
//     `/_next/static/...` asset paths. Iframed at /__convex/, the
//     browser resolved those against the page origin's root —
//     fetched the Synapse Next.js shell's chunks instead of the
//     Convex ones. Iframe stayed black for a different reason.
//   v1.6.12+: revert to the cross-origin URL, but fix the actual
//     gap — Caddy now fronts `{{DOMAIN}}:6791` with TLS so the
//     iframe load succeeds end-to-end. The Convex Dashboard SPA
//     keeps its root-path assumption. postMessage handshake is
//     cross-origin (parent on :443, iframe on :6791) and uses the
//     iframe's exact origin as targetOrigin.
const CONVEX_DASHBOARD_URL =
  process.env.NEXT_PUBLIC_CONVEX_DASHBOARD_URL?.replace(/\/$/, "") ||
  "http://localhost:6791";

// Origin used for the postMessage handshake. Derived from the
// build-time URL above so a misconfigured PUBLIC_CONVEX_DASHBOARD_URL
// can't leak creds to a different page. Falls back to "*" only when
// the URL is unparseable (shouldn't happen in practice — setup.sh
// always renders a valid URL).
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
 * Strategy E (overlay): we render a `<DeploymentPicker>` ABOVE the
 * iframe so operators can switch deployments without leaving the
 * Convex Dashboard view. The picker is part of OUR dashboard
 * (cross-origin to the iframed Convex one), so a switch is a parent
 * navigation — `router.push("/embed/<new>")` re-mounts the iframe
 * with fresh credentials. We don't try to swap creds in-place
 * because that would require a forked self-hosted dashboard;
 * full reload at switch time keeps us off the rebase treadmill.
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

  // Full deployment record — needed for projectId. The /auth endpoint
  // is intentionally narrow (just creds), so we issue a sibling fetch.
  const { data: deployment } = useSWR<Deployment>(
    ["/deployment", name],
    () => api.deployments.get(name),
  );

  // Full project — needed for the picker's "Project page" link, and
  // for the team slug we'd otherwise have to derive from the URL.
  const { data: project } = useSWR<Project>(
    deployment ? ["/project", deployment.projectId!] : null,
    () => api.projects.get(deployment!.projectId!),
  );

  // Sibling deployments under the same project — feeds the picker
  // dropdown. Polled cheaply so a deployment created in another tab
  // shows up in the picker without a manual reload.
  const { data: siblings } = useSWR<Deployment[]>(
    deployment ? ["/sibs", deployment.projectId!] : null,
    () => api.projects.listDeployments(deployment!.projectId!),
    { refreshInterval: 30_000 },
  );

  // Lazy: only resolve the team when we know the project (for the
  // picker's "Project settings" deep-link). Cached separately from
  // the project SWR key so it survives if the project query churns.
  const { data: team } = useSWR<Team>(
    project ? ["/team", project.teamId] : null,
    () => api.teams.get(project!.teamId),
  );

  // Fetch creds once per deployment name. Re-fetched when the
  // operator picks a sibling and `name` changes.
  useEffect(() => {
    let cancelled = false;
    setAuth(null);
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

  // Stamp the localStorage "last viewed at" for this (project,
  // deployment) so the picker dropdown can render "visited 5m ago"
  // hints. Picker reads the same keys; we don't share them through
  // React state because they're cross-tab + cross-page state, not a
  // render-driven concern.
  useEffect(() => {
    if (typeof window === "undefined") return;
    if (!deployment?.projectId || !deployment?.name) return;
    window.localStorage.setItem(
      `synapse.lastViewedAt.${deployment.projectId}.${deployment.name}`,
      String(Date.now()),
    );
  }, [deployment?.projectId, deployment?.name]);

  // Reply to the dashboard's handshake. The iframe sends the request
  // on mount, possibly multiple times until it hears back; we stay
  // subscribed for the lifetime of the page.
  //
  // The iframe runs the same-origin /__convex/* proxy (v1.6.11+), so
  // `event.origin` equals our own origin. We could be strict and
  // pin to window.location.origin literally, but during the embed-
  // page-on-Synapse-vs-iframe-on-Convex transition the message is
  // routed through the iframe's window, whose origin is whatever the
  // proxy serves it at. window.location.origin matches that exactly
  // because the iframe is same-origin.
  useEffect(() => {
    if (!auth) return;
    function handleMessage(event: MessageEvent) {
      // Iframe lives at CONVEX_DASHBOARD_ORIGIN (e.g.
      // `https://synapsepanel.com:6791`). Parent could be at the
      // same origin OR at a `role=dashboard` custom-domain origin
      // (e.g. `https://dashboard.ingreis.com`). In both cases the
      // iframe's messages arrive with origin = the iframe's URL
      // origin, so we strictly compare against that.
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

  if (!auth || !deployment) {
    return (
      <div className="flex min-h-screen items-center justify-center p-8 text-sm text-zinc-400">
        Loading deployment credentials for <code className="ml-1">{name}</code>…
      </div>
    );
  }

  // Render the overlay header + iframe. The header is intentionally
  // thin (h-10) so the iframed dashboard's own header stays visible
  // right below — operators effectively see two headers stacked,
  // ours for "which deployment?" and theirs for "which page within
  // the deployment". A cleaner integration would require forking
  // the upstream image (see CONVEX_DASHBOARD_PICKER_PLAN.md); this
  // is the pragmatic v1.
  return (
    <div className="flex h-screen flex-col bg-neutral-950">
      <header className="flex h-10 shrink-0 items-center gap-3 border-b border-neutral-900 px-3 text-sm">
        {team && project && (
          <nav
            className="flex items-center gap-2 text-xs text-neutral-500"
            aria-label="Breadcrumb"
          >
            <Link
              href={`/teams/${encodeURIComponent(team.slug)}`}
              className="hover:text-neutral-300"
            >
              {team.name}
            </Link>
            <span aria-hidden="true">/</span>
            <Link
              href={`/teams/${encodeURIComponent(team.slug)}/${encodeURIComponent(project.id)}`}
              className="hover:text-neutral-300"
            >
              {project.name}
            </Link>
          </nav>
        )}
        <div className="flex-1" />
        {team && project && (
          <DeploymentPicker
            current={deployment}
            deployments={siblings ?? [deployment]}
            teamRef={team.slug}
            projectId={project.id}
          />
        )}
      </header>
      <iframe
        // v1.6.13+: key={name} forces React to unmount + remount the
        // iframe whenever the picker switches deployments. The src
        // value is the same constant URL across deployments (the
        // Convex Dashboard image at https://<host>:6791), so without
        // a key React reconciles the iframe in place — same DOM
        // node, same src, no reload. The postMessage handshake that
        // injects adminKey + deploymentUrl only fires on iframe
        // mount, so the Convex Dashboard kept using the previous
        // deployment's creds from its own localStorage and surfaced
        // "deployment URL or admin key is invalid" the moment the
        // operator clicked a sibling in the picker.
        key={name}
        ref={iframeRef}
        src={CONVEX_DASHBOARD_URL}
        title={`${name} — Convex Dashboard`}
        className="h-full w-full flex-1 border-0"
        // The dashboard makes XHR calls to the deployment URL; allow
        // same-origin (within the iframe) plus scripts (it's a SPA).
        sandbox="allow-same-origin allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox"
      />
    </div>
  );
}
