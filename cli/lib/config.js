const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

function configPath() {
  if (process.env.SYNAPSE_CLI_CONFIG) {
    return process.env.SYNAPSE_CLI_CONFIG;
  }
  return path.join(os.homedir(), ".synapse", "config.json");
}

function normalizeBaseUrl(raw) {
  const value = String(raw || "").trim();
  if (!value) {
    throw new Error("Synapse URL is required");
  }
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error(`Invalid Synapse URL: ${value}`);
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    throw new Error("Synapse URL must start with http:// or https://");
  }
  parsed.hash = "";
  parsed.search = "";
  parsed.pathname = parsed.pathname.replace(/\/+$/, "");
  return parsed.toString().replace(/\/+$/, "");
}

function readConfig() {
  const file = configPath();
  if (!fs.existsSync(file)) {
    return null;
  }
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch (err) {
    throw new Error(`Could not read ${file}: ${err.message}`);
  }
}

function requireConfig() {
  const cfg = readConfig();
  if (!cfg || !cfg.baseUrl || !cfg.accessToken) {
    throw new Error("Not logged in. Run `synapse login <url>` first.");
  }
  return cfg;
}

function writeConfig(config) {
  const file = configPath();
  const dir = path.dirname(file);
  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  try {
    fs.chmodSync(dir, 0o700);
  } catch {
    // Best-effort on filesystems that do not support POSIX modes.
  }
  const payload = JSON.stringify(config, null, 2) + "\n";
  fs.writeFileSync(file, payload, { mode: 0o600 });
  try {
    fs.chmodSync(file, 0o600);
  } catch {
    // Best-effort on filesystems that do not support POSIX modes.
  }
  return file;
}

function clearConfig() {
  const file = configPath();
  if (fs.existsSync(file)) {
    fs.rmSync(file);
    return true;
  }
  return false;
}

module.exports = {
  clearConfig,
  configPath,
  normalizeBaseUrl,
  readConfig,
  requireConfig,
  writeConfig,
};
