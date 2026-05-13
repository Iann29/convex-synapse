import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Standalone output bundles only the runtime files needed for `node server.js`,
  // making the docker image small (~150MB) and self-contained.
  output: "standalone",
  // Silence the multi-lockfile workspace-root warning by pinning Turbopack
  // to this directory.
  turbopack: {
    root: __dirname,
  },
  // Pin the build ID to the release version. Next.js otherwise generates a
  // random buildId per build, which means a v1.5.4 install rebuilt on a CI
  // shadow run gets a different chunk-name fingerprint than the same release
  // built on the operator's VPS — even though they're the same source. Pinning
  // to NEXT_PUBLIC_DASHBOARD_VERSION (which the Dockerfile bakes from setup.sh's
  // SYNAPSE_VERSION stamp) means: every build of v1.5.4 produces the same
  // chunk URLs, every build of v1.5.5 produces NEW chunk URLs, and the
  // browser's HTML fetch after upgrade always points at fresh hash-named JS.
  // Combined with --force-recreate on compose::up, the whole stale-bundle
  // class of bugs goes away: container is rebuilt, served HTML references
  // new chunks, browser fetches them, version chip updates.
  generateBuildId: async () =>
    process.env.NEXT_PUBLIC_DASHBOARD_VERSION || "dev",
  // Same-origin proxy for /v1/* and /__convex/* during `npm run dev`.
  //
  // v1.6.11+: lib/api.ts uses window.location.origin in the browser
  // (so role='dashboard' custom domains stay same-origin without
  // CORS gymnastics). In production Caddy + synapse-api handle the
  // routing. In dev, Next.js owns the operator's terminal port
  // (typically 6790) and the API runs on a separate one (8080); the
  // rewrites below let `fetch("/v1/...")` Just Work without
  // requiring NEXT_PUBLIC_SYNAPSE_URL to be set.
  //
  // Production: Next.js rewrites still fire in `next start`, but
  // Caddy never sends /v1/* to the dashboard container in the first
  // place (the {{DOMAIN}} block routes it to synapse-api directly),
  // so these are effectively dev-only.
  async rewrites() {
    const apiTarget =
      process.env.SYNAPSE_DEV_API_URL?.replace(/\/$/, "") ||
      "http://localhost:8080";
    const convexTarget =
      process.env.SYNAPSE_DEV_CONVEX_DASHBOARD_URL?.replace(/\/$/, "") ||
      "http://localhost:6791";
    return [
      { source: "/v1/:path*", destination: `${apiTarget}/v1/:path*` },
      { source: "/d/:path*", destination: `${apiTarget}/d/:path*` },
      { source: "/health", destination: `${apiTarget}/health` },
      // /__convex/* is the dashboard image proxy. In dev we bypass
      // synapse-api and hit the convex-dashboard service directly
      // because the dev workflow usually runs the dashboard image
      // standalone (no convex-dashboard-proxy sidecar).
      { source: "/__convex/:path*", destination: `${convexTarget}/:path*` },
    ];
  },
};

export default nextConfig;
