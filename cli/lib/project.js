const fs = require("node:fs");
const path = require("node:path");

const PROJECT_DIR = ".synapse";
const PROJECT_FILE = "project.json";

function projectConfigPath(projectDir = process.cwd()) {
  return path.join(projectDir, PROJECT_DIR, PROJECT_FILE);
}

function compactObject(value) {
  const out = {};
  for (const [key, item] of Object.entries(value)) {
    if (item !== undefined && item !== null && item !== "") {
      out[key] = item;
    }
  }
  return out;
}

function entityRef(entity) {
  if (!entity) {
    return undefined;
  }
  return compactObject({
    id: entity.id,
    slug: entity.slug,
    name: entity.name,
  });
}

function deploymentRef(deployment) {
  if (!deployment) {
    return undefined;
  }
  return compactObject({
    id: deployment.id,
    name: deployment.name,
    deploymentType: deployment.deploymentType || deployment.type,
    isDefault: deployment.isDefault,
    status: deployment.status,
  });
}

function sanitizeProjectConfig(input) {
  const deployments = {};
  if (input.deployments?.dev) {
    deployments.dev = deploymentRef(input.deployments.dev);
  }
  if (input.deployments?.prod) {
    deployments.prod = deploymentRef(input.deployments.prod);
  }
  return compactObject({
    version: 1,
    synapseUrl: input.synapseUrl,
    team: entityRef(input.team),
    project: entityRef(input.project),
    deployments,
  });
}

function buildProjectConfig({ synapseUrl, team, project, deployments }) {
  return sanitizeProjectConfig({
    synapseUrl,
    team,
    project,
    deployments,
  });
}

function writeProjectConfig(projectDir, config) {
  const file = projectConfigPath(projectDir);
  fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
  const safe = sanitizeProjectConfig(config);
  fs.writeFileSync(file, JSON.stringify(safe, null, 2) + "\n", { mode: 0o600 });
  try {
    fs.chmodSync(file, 0o600);
  } catch {
    // Best-effort on filesystems that do not support POSIX modes.
  }
  return file;
}

function readProjectConfig(projectDir = process.cwd()) {
  const file = projectConfigPath(projectDir);
  if (!fs.existsSync(file)) {
    return null;
  }
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch (err) {
    throw new Error(`Could not read ${file}: ${err.message}`);
  }
}

function deploymentNameForTarget(config, target) {
  const deployment = config?.deployments?.[target];
  if (!deployment) {
    return "";
  }
  return typeof deployment === "string" ? deployment : deployment.name || "";
}

module.exports = {
  PROJECT_DIR,
  PROJECT_FILE,
  buildProjectConfig,
  deploymentNameForTarget,
  projectConfigPath,
  readProjectConfig,
  sanitizeProjectConfig,
  writeProjectConfig,
};
