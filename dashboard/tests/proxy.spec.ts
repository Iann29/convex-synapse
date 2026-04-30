import { test, expect, type Page } from "@playwright/test";
import { Client } from "pg";
import { truncateAll } from "./helpers/db";
import { pruneSynapseContainers } from "./helpers/docker";

// Proxy spec — exercises the reverse-proxy mounted at /d/{name}/* on the
// Synapse listener (see synapse/internal/proxy/proxy.go). The compose stack
// enables this via SYNAPSE_PROXY_ENABLED=true; we still gate on /health so a
// stack that came up with the flag flipped off skips cleanly instead of
// failing with a confusing "404 from /d/...".
//
// All locators stay UI-driven; we drop down to the raw fetch + DB only when
// the test needs to compare what the proxy returns against the direct path,
// or wait out the 30s resolver TTL after a delete.

const SYNAPSE_URL =
  process.env.NEXT_PUBLIC_SYNAPSE_URL?.replace(/\/$/, "") ||
  "http://localhost:8080";
const DB_URL =
  process.env.SYNAPSE_DB_URL ||
  "postgres://synapse:synapse@localhost:5432/synapse";

async function setupProject(page: Page) {
  await page.goto("/register");
  await page.locator("#register-email").fill("ian@example.com");
  await page.locator("#register-password").fill("strongpass123");
  await page.locator("#register-name").fill("Ian");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL(/\/teams\b/);

  await page.getByRole("button", { name: "Create team" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("Amage");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: /amage/i }).click();

  await page.getByRole("button", { name: "Create project" }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#project-name").fill("Store");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: /store/i }).click();

  await expect(page).toHaveURL(/\/teams\/amage\/[0-9a-f-]{36}\b/);
}

// Pulls the JWT the dashboard stashed in localStorage so we can call the
// Synapse REST API directly to fetch /auth (deployment URL + admin key).
// lib/auth.ts persists the AuthBundle under "synapse.auth".
async function bearerFromPage(page: Page): Promise<string> {
  const token = await page.evaluate(() => {
    const raw = localStorage.getItem("synapse.auth");
    if (!raw) return "";
    try {
      const j = JSON.parse(raw) as { accessToken?: string };
      return j.accessToken ?? "";
    } catch {
      return "";
    }
  });
  if (!token) throw new Error("could not extract bearer token from page");
  return token;
}

let proxyEnabled = false;

test.beforeAll(async ({ request }) => {
  // /health surfaces proxyEnabled (see synapse/internal/api/health.go).
  // Skip the whole spec gracefully when the deployed Synapse hasn't been
  // started with the proxy mounted — keeps CI honest without forcing a
  // restart dance inside the test.
  const r = await request.get(`${SYNAPSE_URL}/health`);
  const j = (await r.json()) as { proxyEnabled?: boolean };
  proxyEnabled = !!j.proxyEnabled;
});

test.beforeEach(async () => {
  await truncateAll();
  pruneSynapseContainers();
});

test.afterEach(async () => {
  pruneSynapseContainers();
});

test("proxy forwards /d/{name}/version and 404s missing/deleted deployments", async ({
  page,
  request,
}) => {
  test.skip(!proxyEnabled, "proxy mode disabled (SYNAPSE_PROXY_ENABLED=false)");

  await setupProject(page);

  // Provision via the dashboard so the row + container go through the same
  // path real users hit, including async healthcheck wait.
  await page.getByRole("button", { name: /create deployment/i }).first().click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create", exact: true })
    .click();

  // Wait for the row to appear and the deployment to flip to "running" — the
  // poll loop on the project page swaps the badge once Synapse marks it.
  const nameLocator = page.getByText(/-[a-z]+-\d{4}/).first();
  await expect(nameLocator).toBeVisible({ timeout: 90_000 });
  const deploymentName = (await nameLocator.textContent())?.trim() ?? "";
  expect(deploymentName).toMatch(/^[a-z]+-[a-z]+-\d{4}$/);

  // Wait for the deployment to flip to "running" before hitting either the
  // proxy or the direct URL — both are racy until the backend is up. Only
  // one deployment exists in this spec so a global match is unambiguous.
  await expect(page.getByText("running", { exact: true })).toBeVisible({
    timeout: 90_000,
  });

  // Pull the deployment URL out of the auth endpoint; we'll diff it against
  // the proxy's response. (deploymentUrl is always the host-port mapping —
  // see synapse/internal/docker/provisioner.go.)
  const bearer = await bearerFromPage(page);
  const authResp = await request.get(
    `${SYNAPSE_URL}/v1/deployments/${encodeURIComponent(deploymentName)}/auth`,
    { headers: { Authorization: `Bearer ${bearer}` } },
  );
  expect(authResp.ok()).toBeTruthy();
  const auth = (await authResp.json()) as { deploymentUrl: string };
  expect(auth.deploymentUrl).toMatch(/^http:\/\/127\.0\.0\.1:\d+$/);

  // Hit /d/{name}/version through the proxy and via the direct URL — both
  // should return the same body (the convex backend's /version endpoint).
  const proxyURL = `${SYNAPSE_URL}/d/${deploymentName}/version`;
  const directURL = `${auth.deploymentUrl}/version`;

  const [viaProxy, viaDirect] = await Promise.all([
    request.get(proxyURL),
    request.get(directURL),
  ]);
  expect(viaProxy.status()).toBe(viaDirect.status());
  expect(viaProxy.status()).toBeLessThan(500);
  expect(await viaProxy.text()).toBe(await viaDirect.text());

  // Unknown deployment → 404 with code: deployment_not_found.
  const missing = await request.get(`${SYNAPSE_URL}/d/does-not-exist/version`);
  expect(missing.status()).toBe(404);
  const missingBody = (await missing.json()) as { code?: string };
  expect(missingBody.code).toBe("deployment_not_found");

  // Delete via the dashboard, then poll the proxy URL until it 404s. The
  // resolver caches name→address for ~30s, so allow up to 35s before failing.
  page.on("dialog", (d) => d.accept());
  await page
    .getByRole("button", {
      name: new RegExp(`delete deployment ${deploymentName}`, "i"),
    })
    .click();

  // The DB row flips to status='deleted' immediately; the proxy may still
  // resolve from cache until TTL. Polling avoids a flaky one-shot check.
  // (Alternative: query the resolver's DB row directly to verify status.)
  await expect
    .poll(
      async () => {
        const r = await request.get(proxyURL, {
          // request-level retries off; we want the raw status each iteration.
          maxRedirects: 0,
        });
        return r.status();
      },
      { timeout: 35_000, intervals: [1000, 2000, 3000] },
    )
    .toBe(404);

  // Sanity: the row really did flip in the DB, so the proxy 404 isn't a
  // false positive from a network blip. The deployments table never hard-
  // deletes — `delete` flips status to 'deleted' so the row sticks around.
  const client = new Client({ connectionString: DB_URL });
  await client.connect();
  try {
    const r = await client.query<{ status: string }>(
      `SELECT status FROM deployments WHERE name = $1`,
      [deploymentName],
    );
    expect(r.rows[0]?.status).toBe("deleted");
  } finally {
    await client.end();
  }
});
