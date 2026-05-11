const assert = require("node:assert/strict");
const test = require("node:test");

const { buildConvexEnv } = require("../lib/convex");

test("removes CONVEX_DEPLOYMENT when self-hosted credentials are present", () => {
  const env = buildConvexEnv({
    CONVEX_DEPLOYMENT: "dev:cloud-cat",
    CONVEX_SELF_HOSTED_URL: "https://happy-cat.convex.example.com",
    CONVEX_SELF_HOSTED_ADMIN_KEY: "dev:happy-cat|secret",
  });
  assert.equal(env.CONVEX_DEPLOYMENT, undefined);
  assert.equal(env.CONVEX_SELF_HOSTED_URL, "https://happy-cat.convex.example.com");
});

test("uses project .env.local self-hosted credentials to clean child env", () => {
  const env = buildConvexEnv(
    { CONVEX_DEPLOYMENT: "dev:cloud-cat" },
    {
      CONVEX_SELF_HOSTED_URL: "https://happy-cat.convex.example.com",
      CONVEX_SELF_HOSTED_ADMIN_KEY: "dev:happy-cat|secret",
    },
  );
  assert.equal(env.CONVEX_DEPLOYMENT, undefined);
  assert.equal(env.CONVEX_SELF_HOSTED_URL, "https://happy-cat.convex.example.com");
  assert.equal(env.CONVEX_SELF_HOSTED_ADMIN_KEY, "dev:happy-cat|secret");
});

test("runtime credentials override shell and project self-hosted env", () => {
  const env = buildConvexEnv(
    {
      CONVEX_DEPLOYMENT: "dev:cloud-cat",
      CONVEX_SELF_HOSTED_URL: "https://old.example.com",
      CONVEX_SELF_HOSTED_ADMIN_KEY: "dev:old|secret",
    },
    {
      CONVEX_SELF_HOSTED_URL: "https://project.example.com",
      CONVEX_SELF_HOSTED_ADMIN_KEY: "dev:project|secret",
    },
    {
      CONVEX_SELF_HOSTED_URL: "https://target.example.com",
      CONVEX_SELF_HOSTED_ADMIN_KEY: "prod:target|secret",
    },
  );
  assert.equal(env.CONVEX_DEPLOYMENT, undefined);
  assert.equal(env.CONVEX_SELF_HOSTED_URL, "https://target.example.com");
  assert.equal(env.CONVEX_SELF_HOSTED_ADMIN_KEY, "prod:target|secret");
});

test("leaves CONVEX_DEPLOYMENT alone without complete self-hosted credentials", () => {
  const env = buildConvexEnv({
    CONVEX_DEPLOYMENT: "dev:cloud-cat",
    CONVEX_SELF_HOSTED_URL: "https://happy-cat.convex.example.com",
  });
  assert.equal(env.CONVEX_DEPLOYMENT, "dev:cloud-cat");
});
