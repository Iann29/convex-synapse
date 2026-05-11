const fs = require("node:fs");
const path = require("node:path");

const SELF_HOSTED_URL = "CONVEX_SELF_HOSTED_URL";
const SELF_HOSTED_ADMIN_KEY = "CONVEX_SELF_HOSTED_ADMIN_KEY";
const CONVEX_DEPLOYMENT = "CONVEX_DEPLOYMENT";

function quoteEnvValue(value) {
  return `"${String(value).replace(/\\/g, "\\\\").replace(/"/g, '\\"').replace(/\n/g, "\\n")}"`;
}

function envAssignment(name, value) {
  return `${name}=${quoteEnvValue(value)}`;
}

function keyFromLine(line) {
  const match = line.match(/^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=/);
  return match ? match[1] : null;
}

function commentDeploymentLine(line) {
  if (/^\s*#/.test(line)) {
    return line;
  }
  return `# ${line} # disabled by synapse CLI for self-hosted Convex`;
}

function unquoteEnvValue(raw) {
  const value = String(raw || "").trim();
  if (
    (value.startsWith('"') && value.endsWith('"')) ||
    (value.startsWith("'") && value.endsWith("'"))
  ) {
    return value.slice(1, -1);
  }
  return value;
}

function parseEnvContent(content) {
  const out = {};
  for (const line of String(content || "").split(/\r?\n/)) {
    if (/^\s*(?:#|$)/.test(line)) {
      continue;
    }
    const key = keyFromLine(line);
    if (!key) {
      continue;
    }
    const valueStart = line.indexOf("=");
    if (valueStart < 0) {
      continue;
    }
    out[key] = unquoteEnvValue(line.slice(valueStart + 1));
  }
  return out;
}

function readProjectEnv(projectDir) {
  const file = path.join(projectDir, ".env.local");
  if (!fs.existsSync(file)) {
    return {};
  }
  return parseEnvContent(fs.readFileSync(file, "utf8"));
}

function updateEnvContent(content, { convexUrl, adminKey }) {
  const lines = content ? content.split(/\r?\n/) : [];
  if (lines.length > 0 && lines[lines.length - 1] === "") {
    lines.pop();
  }

  const replacements = new Map([
    [SELF_HOSTED_URL, envAssignment(SELF_HOSTED_URL, convexUrl)],
    [SELF_HOSTED_ADMIN_KEY, envAssignment(SELF_HOSTED_ADMIN_KEY, adminKey)],
  ]);
  const seen = new Set();
  const out = [];

  for (const line of lines) {
    const key = keyFromLine(line);
    if (key === CONVEX_DEPLOYMENT) {
      out.push(commentDeploymentLine(line));
      continue;
    }
    if (replacements.has(key)) {
      if (!seen.has(key)) {
        out.push(replacements.get(key));
        seen.add(key);
      }
      continue;
    }
    out.push(line);
  }

  if (out.length > 0 && out[out.length - 1] !== "") {
    out.push("");
  }
  for (const [key, line] of replacements.entries()) {
    if (!seen.has(key)) {
      out.push(line);
    }
  }

  return out.join("\n") + "\n";
}

function writeProjectEnv(projectDir, credentials) {
  const file = path.join(projectDir, ".env.local");
  const existing = fs.existsSync(file) ? fs.readFileSync(file, "utf8") : "";
  const next = updateEnvContent(existing, {
    convexUrl: credentials.convexUrl,
    adminKey: credentials.adminKey,
  });
  fs.writeFileSync(file, next, { mode: 0o600 });
  try {
    fs.chmodSync(file, 0o600);
  } catch {
    // Best-effort on filesystems that do not support POSIX modes.
  }
  return file;
}

module.exports = {
  CONVEX_DEPLOYMENT,
  SELF_HOSTED_ADMIN_KEY,
  SELF_HOSTED_URL,
  keyFromLine,
  parseEnvContent,
  quoteEnvValue,
  readProjectEnv,
  updateEnvContent,
  writeProjectEnv,
};
