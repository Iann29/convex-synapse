const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const { clientFromConfig } = require("../bin/synapse");
const config = require("../lib/config");

test("clientFromConfig refreshes an expired access token and retries", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "synapse-bin-"));
  process.env.SYNAPSE_CLI_CONFIG = path.join(dir, "config.json");
  const originalFetch = global.fetch;
  const calls = [];
  try {
    config.writeConfig({
      baseUrl: "https://synapse.example.com",
      accessToken: "expired",
      refreshToken: "refresh",
    });
    global.fetch = async (url, init) => {
      calls.push({ path: url.pathname, auth: init.headers.Authorization });
      if (url.pathname === "/v1/me/" && init.headers.Authorization === "Bearer expired") {
        return new Response(JSON.stringify({ code: "invalid_token", message: "expired" }), {
          status: 401,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (url.pathname === "/v1/auth/refresh") {
        assert.equal(init.headers.Authorization, undefined);
        assert.deepEqual(JSON.parse(init.body), { refreshToken: "refresh" });
        return new Response(JSON.stringify({ accessToken: "fresh", refreshToken: "fresh-refresh" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (url.pathname === "/v1/me/" && init.headers.Authorization === "Bearer fresh") {
        return new Response(JSON.stringify({ email: "ian@example.com" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      throw new Error(`unexpected call ${url.pathname}`);
    };

    const { api } = clientFromConfig();
    const me = await api.me();
    assert.equal(me.email, "ian@example.com");
    assert.deepEqual(calls.map((c) => c.path), ["/v1/me/", "/v1/auth/refresh", "/v1/me/"]);
    assert.equal(config.readConfig().accessToken, "fresh");
    assert.equal(config.readConfig().refreshToken, "fresh-refresh");
  } finally {
    global.fetch = originalFetch;
    delete process.env.SYNAPSE_CLI_CONFIG;
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
