const { spawn } = require("node:child_process");
const { readProjectEnv } = require("./env-file");

function envFromCredentials(credentials) {
  if (!credentials) {
    return {};
  }
  return {
    CONVEX_SELF_HOSTED_URL: credentials.convexUrl,
    CONVEX_SELF_HOSTED_ADMIN_KEY: credentials.adminKey,
  };
}

function buildConvexEnv(source = process.env, projectEnv = {}, overrides = {}) {
  const env = { ...source };
  for (const key of ["CONVEX_SELF_HOSTED_URL", "CONVEX_SELF_HOSTED_ADMIN_KEY"]) {
    if (!env[key] && projectEnv[key]) {
      env[key] = projectEnv[key];
    }
  }
  for (const [key, value] of Object.entries(overrides)) {
    if (value) {
      env[key] = value;
    }
  }
  if (env.CONVEX_SELF_HOSTED_URL && env.CONVEX_SELF_HOSTED_ADMIN_KEY) {
    delete env.CONVEX_DEPLOYMENT;
  }
  return env;
}

function runConvex(args, { env = process.env, stdio = "inherit", credentials = null, spawnImpl = spawn } = {}) {
  const executable = process.platform === "win32" ? "npx.cmd" : "npx";
  const projectEnv = readProjectEnv(process.cwd());
  const child = spawnImpl(executable, ["convex", ...args], {
    env: buildConvexEnv(env, projectEnv, envFromCredentials(credentials)),
    stdio,
  });

  return new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("close", (code, signal) => {
      if (signal) {
        resolve(1);
        return;
      }
      resolve(code ?? 1);
    });
  });
}

module.exports = {
  buildConvexEnv,
  envFromCredentials,
  runConvex,
};
