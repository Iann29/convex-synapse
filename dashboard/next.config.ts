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
};

export default nextConfig;
