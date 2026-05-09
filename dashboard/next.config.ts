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
  generateBuildId: async () => process.env.NEXT_PUBLIC_DASHBOARD_VERSION || "dev",
};

export default nextConfig;
