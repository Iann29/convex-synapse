import { test, expect, type Page } from "@playwright/test";
import { Client } from "pg";
import { truncateAll } from "./helpers/db";
import {
  listSynapseContainerNames,
  pruneSynapseContainers,
} from "./helpers/docker";

// Multi-deployment provisioning — exercises:
//   - the "New deployment" button at the top of the project page (the empty-
//     state "Create deployment" only renders when there are zero rows; once
//     the first row exists, we drive the second/third via the header button)
//   - the SWR refresh loop that waits on status="running" before the page
//     stops polling
//   - the port allocator picking distinct host ports for concurrent tenants
//   - bulk delete cleanup leaving zero synapse-managed containers behind
//
// Catches port-allocation races and multi-row rendering regressions in a
// single shot.

const DB_URL =
  process.env.SYNAPSE_DB_URL ||
  "postgres://synapse:synapse@localhost:5432/synapse";

const PORT_RANGE_MIN = Number(process.env.SYNAPSE_PORT_RANGE_MIN || 3210);
const PORT_RANGE_MAX = Number(process.env.SYNAPSE_PORT_RANGE_MAX || 3500);

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

// Drives the create-deployment dialog. The first call hits the empty-state
// "Create deployment" button; subsequent calls hit the header "New deployment"
// button. Both end up on the same dialog whose "Create" button submits.
async function createDeployment(page: Page, type: "dev" | "prod") {
  // Prefer the header button (always present); fall back to the empty-state
  // one if the header isn't rendered yet (first deployment, slow first paint).
  const headerBtn = page.getByRole("button", { name: "New deployment" });
  const emptyBtn = page.getByRole("button", { name: "Create deployment" });
  if (await headerBtn.isVisible().catch(() => false)) {
    await headerBtn.click();
  } else {
    await emptyBtn.click();
  }

  const dialog = page.getByRole("dialog");
  // Type toggle inside the dialog — buttons are labelled "dev"/"prod".
  await dialog.getByRole("button", { name: type, exact: true }).click();
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  // Dialog closes on success; if it lingers, the create errored.
  await expect(dialog).toBeHidden({ timeout: 15_000 });
}

async function activeHostPorts(): Promise<number[]> {
  const client = new Client({ connectionString: DB_URL });
  await client.connect();
  try {
    const r = await client.query<{ host_port: number }>(
      `SELECT host_port FROM deployments
        WHERE status <> 'deleted' AND host_port IS NOT NULL
        ORDER BY host_port`,
    );
    return r.rows.map((row) => row.host_port);
  } finally {
    await client.end();
  }
}

test.beforeEach(async () => {
  await truncateAll();
  pruneSynapseContainers();
});

test.afterEach(async () => {
  pruneSynapseContainers();
});

test("provision three deployments, then delete them all", async ({ page }) => {
  await setupProject(page);

  // Sequence: dev, prod, dev. Each create call waits for the dialog to
  // close; the SWR poller keeps the page in sync as rows arrive.
  await createDeployment(page, "dev");
  await createDeployment(page, "prod");
  await createDeployment(page, "dev");

  // Three rows visible — each row's title uses the friendly "<adj>-<animal>-NNNN"
  // pattern. We rely on count() rather than per-name lookups so the assertion
  // doesn't have to know what names the allocator picked.
  const nameRowsSel = page.getByText(/^[a-z]+-[a-z]+-\d{4}$/);
  await expect(nameRowsSel).toHaveCount(3, { timeout: 30_000 });

  const names = (await nameRowsSel.allTextContents()).map((s) => s.trim());
  expect(new Set(names).size).toBe(3);

  // Wait for all three to flip to status="running". The page renders three
  // status badges; they start as "provisioning" and the SWR loop swaps them.
  // Worker processes jobs serially (one Provision at a time) and may be
  // catching up on orphans from prior tests that left jobs in flight
  // during truncate. Keep the timeout generous.
  await expect(page.getByText("running", { exact: true })).toHaveCount(3, {
    timeout: 180_000,
  });

  // Three distinct convex-* containers on the host.
  await expect
    .poll(() => listSynapseContainerNames().filter((n) => n.startsWith("convex-")).length, {
      timeout: 30_000,
      intervals: [500, 1000, 2000],
    })
    .toBe(3);
  const containers = listSynapseContainerNames();
  for (const name of names) {
    expect(containers).toContain(`convex-${name}`);
  }

  // Distinct host ports, all inside the configured allocator range.
  const ports = await activeHostPorts();
  expect(ports).toHaveLength(3);
  expect(new Set(ports).size).toBe(3);
  for (const p of ports) {
    expect(p).toBeGreaterThanOrEqual(PORT_RANGE_MIN);
    expect(p).toBeLessThanOrEqual(PORT_RANGE_MAX);
  }

  // Delete all three via the UI — the order doesn't matter; each click hits
  // a per-row "delete deployment {name}" button. Auto-accept the native
  // confirm() that the delete handler raises.
  page.on("dialog", (d) => d.accept());
  for (const name of names) {
    await page
      .getByRole("button", {
        name: new RegExp(`delete deployment ${name}`, "i"),
      })
      .click();
    // Wait for the row to vanish before deleting the next — sequential
    // deletes mirror what a human operator would do and avoid stacking
    // confirm() dialogs.
    await expect(page.getByText(name, { exact: true })).toBeHidden({
      timeout: 15_000,
    });
  }

  // Empty list, empty container set.
  await expect
    .poll(() => listSynapseContainerNames().length, { timeout: 30_000 })
    .toBe(0);
  expect(await activeHostPorts()).toEqual([]);
});
