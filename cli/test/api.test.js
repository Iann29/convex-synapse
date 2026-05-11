const assert = require("node:assert/strict");
const test = require("node:test");

const { SynapseAPI, SynapseAPIError } = require("../lib/api");

function jsonResponse(status, data) {
  return new Response(JSON.stringify(data), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

test("login posts to Synapse auth endpoint without bearer", async () => {
  const api = new SynapseAPI({
    baseUrl: "https://synapse.example.com",
    fetchImpl: async (url, init) => {
      assert.equal(url.pathname, "/v1/auth/login");
      assert.equal(init.method, "POST");
      assert.equal(init.headers.Authorization, undefined);
      assert.deepEqual(JSON.parse(init.body), { email: "ian@example.com", password: "secret" });
      return jsonResponse(200, { accessToken: "jwt", tokenType: "Bearer", user: { email: "ian@example.com" } });
    },
  });
  const got = await api.login("ian@example.com", "secret");
  assert.equal(got.accessToken, "jwt");
});

test("authenticated requests send bearer token", async () => {
  const api = new SynapseAPI({
    baseUrl: "https://synapse.example.com",
    accessToken: "tok",
    fetchImpl: async (url, init) => {
      assert.equal(url.pathname, "/v1/me/");
      assert.equal(init.headers.Authorization, "Bearer tok");
      return jsonResponse(200, { email: "ian@example.com" });
    },
  });
  const got = await api.me();
  assert.equal(got.email, "ian@example.com");
});

test("list endpoints follow X-Next-Cursor pagination", async () => {
  const seen = [];
  const api = new SynapseAPI({
    baseUrl: "https://synapse.example.com",
    accessToken: "tok",
    fetchImpl: async (url, init) => {
      seen.push(`${url.pathname}${url.search}`);
      assert.equal(init.headers.Authorization, "Bearer tok");
      if (url.searchParams.get("cursor") === "2") {
        return new Response(JSON.stringify([{ id: "3", slug: "third" }]), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(JSON.stringify([{ id: "1", slug: "first" }, { id: "2", slug: "second" }]), {
        status: 200,
        headers: {
          "Content-Type": "application/json",
          "X-Next-Cursor": "2",
        },
      });
    },
  });

  const teams = await api.teams();
  assert.deepEqual(teams.map((team) => team.slug), ["first", "second", "third"]);
  assert.deepEqual(seen, ["/v1/teams/?limit=500", "/v1/teams/?limit=500&cursor=2"]);
});

test("refresh posts refreshToken without bearer", async () => {
  const api = new SynapseAPI({
    baseUrl: "https://synapse.example.com",
    accessToken: "expired",
    fetchImpl: async (url, init) => {
      assert.equal(url.pathname, "/v1/auth/refresh");
      assert.equal(init.method, "POST");
      assert.equal(init.headers.Authorization, undefined);
      assert.deepEqual(JSON.parse(init.body), { refreshToken: "refresh" });
      return jsonResponse(200, { accessToken: "new", refreshToken: "new-refresh" });
    },
  });
  const got = await api.refresh("refresh");
  assert.equal(got.accessToken, "new");
});

test("API errors include stable code and status", async () => {
  const api = new SynapseAPI({
    baseUrl: "https://synapse.example.com",
    accessToken: "tok",
    fetchImpl: async () => jsonResponse(403, { code: "forbidden", message: "Nope" }),
  });
  await assert.rejects(() => api.teams(), (err) => {
    assert.ok(err instanceof SynapseAPIError);
    assert.equal(err.status, 403);
    assert.equal(err.code, "forbidden");
    assert.equal(err.message, "Nope");
    return true;
  });
});

test("network failures get a Synapse-specific error", async () => {
  const api = new SynapseAPI({
    baseUrl: "https://synapse.example.com",
    fetchImpl: async () => {
      throw new Error("ECONNREFUSED");
    },
  });
  await assert.rejects(() => api.me(), (err) => {
    assert.ok(err instanceof SynapseAPIError);
    assert.equal(err.status, 0);
    assert.equal(err.code, "network_error");
    assert.match(err.message, /Could not reach Synapse/);
    return true;
  });
});

test("successful non-JSON responses get a stable bad_response error", async () => {
  const api = new SynapseAPI({
    baseUrl: "https://synapse.example.com",
    fetchImpl: async () => new Response("not json", { status: 200 }),
  });
  await assert.rejects(() => api.me(), (err) => {
    assert.ok(err instanceof SynapseAPIError);
    assert.equal(err.status, 200);
    assert.equal(err.code, "bad_response");
    assert.match(err.message, /did not return JSON/);
    return true;
  });
});
