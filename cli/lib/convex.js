const { spawn } = require("node:child_process");
const { readProjectEnv } = require("./env-file");

function buildConvexEnv(source = process.env, projectEnv = {}) {
  const env = { ...source };
  for (const key of ["CONVEX_SELF_HOSTED_URL", "CONVEX_SELF_HOSTED_ADMIN_KEY"]) {
    if (!env[key] && projectEnv[key]) {
      env[key] = projectEnv[key];
    }
  }
  if (env.CONVEX_SELF_HOSTED_URL && env.CONVEX_SELF_HOSTED_ADMIN_KEY) {
    delete env.CONVEX_DEPLOYMENT;
  }
  return env;
}

function runConvex(args, { env = process.env, stdio = "inherit" } = {}) {
  const executable = process.platform === "win32" ? "npx.cmd" : "npx";
  const projectEnv = readProjectEnv(process.cwd());
  const child = spawn(executable, ["convex", ...args], {
    env: buildConvexEnv(env, projectEnv),
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
  runConvex,
};
