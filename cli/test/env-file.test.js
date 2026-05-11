const assert = require("node:assert/strict");
const test = require("node:test");

const { keyFromLine, parseEnvContent, updateEnvContent } = require("../lib/env-file");

test("detects env keys from plain and export assignments", () => {
  assert.equal(keyFromLine("CONVEX_DEPLOYMENT=dev:old"), "CONVEX_DEPLOYMENT");
  assert.equal(keyFromLine(" export CONVEX_SELF_HOSTED_URL=x"), "CONVEX_SELF_HOSTED_URL");
  assert.equal(keyFromLine("# CONVEX_DEPLOYMENT=dev:old"), null);
});

test("updates self-hosted env vars and comments CONVEX_DEPLOYMENT", () => {
  const existing = [
    "FOO=bar",
    "CONVEX_DEPLOYMENT=dev:old-wolf-123",
    "CONVEX_SELF_HOSTED_URL=http://old",
    "",
  ].join("\n");
  const next = updateEnvContent(existing, {
    convexUrl: "https://happy-cat.convex.example.com",
    adminKey: "dev:happy-cat|secret",
  });

  assert.match(next, /FOO=bar/);
  assert.match(next, /# CONVEX_DEPLOYMENT=dev:old-wolf-123 # disabled by synapse CLI/);
  assert.match(next, /CONVEX_SELF_HOSTED_URL="https:\/\/happy-cat\.convex\.example\.com"/);
  assert.match(next, /CONVEX_SELF_HOSTED_ADMIN_KEY="dev:happy-cat\|secret"/);
  assert.doesNotMatch(next, /^CONVEX_DEPLOYMENT=/m);
});

test("parses .env.local values needed by delegated convex commands", () => {
  const parsed = parseEnvContent([
    "# ignored",
    "CONVEX_DEPLOYMENT=dev:cloud",
    'CONVEX_SELF_HOSTED_URL="https://happy-cat.convex.example.com"',
    "CONVEX_SELF_HOSTED_ADMIN_KEY='dev:happy-cat|secret'",
  ].join("\n"));

  assert.equal(parsed.CONVEX_DEPLOYMENT, "dev:cloud");
  assert.equal(parsed.CONVEX_SELF_HOSTED_URL, "https://happy-cat.convex.example.com");
  assert.equal(parsed.CONVEX_SELF_HOSTED_ADMIN_KEY, "dev:happy-cat|secret");
});

test("does not duplicate managed keys", () => {
  const next = updateEnvContent(
    [
      "CONVEX_SELF_HOSTED_URL=http://old",
      "CONVEX_SELF_HOSTED_URL=http://duplicate",
      "CONVEX_SELF_HOSTED_ADMIN_KEY=old",
    ].join("\n"),
    { convexUrl: "http://new", adminKey: "new-key" },
  );
  assert.equal((next.match(/^CONVEX_SELF_HOSTED_URL=/gm) || []).length, 1);
  assert.equal((next.match(/^CONVEX_SELF_HOSTED_ADMIN_KEY=/gm) || []).length, 1);
});
