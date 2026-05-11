#!/usr/bin/env node

const { SynapseAPI, SynapseAPIError } = require("../lib/api");
const { clearConfig, normalizeBaseUrl, requireConfig, writeConfig } = require("../lib/config");
const { quoteEnvValue, writeProjectEnv } = require("../lib/env-file");
const { askCredentials, choose } = require("../lib/prompts");
const { runConvex } = require("../lib/convex");

function usage() {
  return `Usage:
  synapse login <url>
  synapse logout
  synapse whoami
  synapse select
  synapse credentials <deployment> [--format env|shell|json]
  synapse convex [...args]
`;
}

function clientFromConfig() {
  const cfg = requireConfig();
  const api = new SynapseAPI({ baseUrl: cfg.baseUrl, accessToken: cfg.accessToken });
  const refreshable = new Proxy(api, {
    get(target, prop) {
      const value = target[prop];
      if (typeof value !== "function") {
        return value;
      }
      return async (...args) => {
        try {
          return await value.apply(target, args);
        } catch (err) {
          if (!(err instanceof SynapseAPIError) || err.status !== 401 || !cfg.refreshToken) {
            throw err;
          }
          const session = await new SynapseAPI({ baseUrl: cfg.baseUrl }).refresh(cfg.refreshToken);
          if (!session.accessToken) {
            throw err;
          }
          cfg.accessToken = session.accessToken;
          cfg.refreshToken = session.refreshToken || cfg.refreshToken;
          cfg.tokenType = session.tokenType || cfg.tokenType || "Bearer";
          if (session.user) {
            cfg.user = session.user;
          }
          writeConfig(cfg);
          target.accessToken = cfg.accessToken;
          return await value.apply(target, args);
        }
      };
    },
  });
  return {
    cfg,
    api: refreshable,
  };
}

function labelName(item) {
  const name = item.name || item.slug || item.id;
  const slug = item.slug && item.slug !== name ? ` (${item.slug})` : "";
  return `${name}${slug}`;
}

function teamRef(team) {
  return team.slug || team.id;
}

function deploymentLabel(deployment) {
  const bits = [deployment.name];
  if (deployment.deploymentType || deployment.type) {
    bits.push(deployment.deploymentType || deployment.type);
  }
  if (deployment.status) {
    bits.push(deployment.status);
  }
  return bits.filter(Boolean).join(" - ");
}

function formatCredentials(creds, format) {
  switch (format) {
    case "json":
      return JSON.stringify(creds, null, 2);
    case "shell":
      return creds.exportSnippet;
    case "env":
      return creds.envSnippet || `CONVEX_SELF_HOSTED_URL=${quoteEnvValue(creds.convexUrl)}\nCONVEX_SELF_HOSTED_ADMIN_KEY=${quoteEnvValue(creds.adminKey)}`;
    default:
      throw new Error("format must be one of: env, shell, json");
  }
}

function parseFormat(args) {
  let format = "env";
  const rest = [];
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (arg === "--format") {
      format = args[i + 1];
      i += 1;
    } else if (arg.startsWith("--format=")) {
      format = arg.slice("--format=".length);
    } else {
      rest.push(arg);
    }
  }
  return { format, rest };
}

async function login(args) {
  const url = args[0];
  if (!url) {
    throw new Error("Usage: synapse login <url>");
  }
  const baseUrl = normalizeBaseUrl(url);
  const { email, password } = await askCredentials();
  const api = new SynapseAPI({ baseUrl });
  const session = await api.login(email, password);
  if (!session.accessToken) {
    throw new Error("Synapse login response did not include accessToken");
  }
  const file = writeConfig({
    baseUrl,
    accessToken: session.accessToken,
    refreshToken: session.refreshToken || null,
    tokenType: session.tokenType || "Bearer",
    user: session.user || null,
  });
  process.stderr.write(`Saved Synapse session to ${file}\n`);
}

async function logout() {
  const removed = clearConfig();
  process.stderr.write(removed ? "Logged out of Synapse.\n" : "No Synapse session was saved.\n");
}

async function whoami() {
  const { cfg, api } = clientFromConfig();
  const me = await api.me();
  const email = me.email || me.user?.email || "(unknown email)";
  const name = me.name || me.user?.name || "";
  process.stdout.write(`${name ? `${name} ` : ""}<${email}> on ${cfg.baseUrl}\n`);
}

async function selectDeployment() {
  const { api } = clientFromConfig();
  const teams = await api.teams();
  const team = await choose("teams", teams.map((t) => ({ label: labelName(t), value: t })));
  const projects = await api.projects(teamRef(team));
  const project = await choose("projects", projects.map((p) => ({ label: labelName(p), value: p })));
  const deployments = await api.deployments(project.id);
  const deployment = await choose(
    "deployments",
    deployments.map((d) => ({ label: deploymentLabel(d), value: d })),
  );
  const creds = await api.cliCredentials(deployment.name);
  const envPath = writeProjectEnv(process.cwd(), creds);
  process.stderr.write(`Selected ${deployment.name}. Updated ${envPath}.\n`);
  if (process.env.CONVEX_DEPLOYMENT) {
    process.stderr.write("Warning: shell CONVEX_DEPLOYMENT is set. Use `synapse convex ...` or unset it before running `npx convex` directly.\n");
  }
}

async function credentials(args) {
  const { format, rest } = parseFormat(args);
  const deployment = rest[0];
  if (!deployment) {
    throw new Error("Usage: synapse credentials <deployment> [--format env|shell|json]");
  }
  if (!["env", "shell", "json"].includes(format)) {
    throw new Error("format must be one of: env, shell, json");
  }
  const { api } = clientFromConfig();
  const creds = await api.cliCredentials(deployment);
  process.stdout.write(formatCredentials(creds, format) + "\n");
}

async function convex(args) {
  const code = await runConvex(args);
  process.exitCode = code;
}

async function main(argv) {
  const [command, ...args] = argv;
  switch (command) {
    case "login":
      return await login(args);
    case "logout":
      return await logout();
    case "whoami":
      return await whoami();
    case "select":
      return await selectDeployment();
    case "credentials":
      return await credentials(args);
    case "convex":
      return await convex(args);
    case "-h":
    case "--help":
    case "help":
    case undefined:
      process.stdout.write(usage());
      return;
    default:
      throw new Error(`Unknown command: ${command}\n\n${usage()}`);
  }
}

if (require.main === module) {
  main(process.argv.slice(2)).catch((err) => {
    process.stderr.write(`${err.message}\n`);
    process.exitCode = 1;
  });
}

module.exports = {
  clientFromConfig,
  formatCredentials,
  main,
  parseFormat,
};
