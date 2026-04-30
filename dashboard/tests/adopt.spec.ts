import { test, expect, type Page } from "@playwright/test";
import { Client } from "pg";
import { truncateAll } from "./helpers/db";
import {
  listSynapseContainerNames,
  pruneSynapseContainers,
} from "./helpers/docker";

const DB_URL =
  process.env.SYNAPSE_DB_URL ||
  "postgres://synapse:synapse@localhost:5432/synapse";

// adoptedNames returns deployment names with adopted=true. We read directly
// from the DB to avoid coupling the assertion to dashboard rendering choices.
async function adoptedNames(): Promise<string[]> {
  const c = new Client({ connectionString: DB_URL });
  await c.connect();
  try {
    const r = await c.query<{ name: string }>(
      `SELECT name FROM deployments WHERE adopted = true AND status <> 'deleted'`,
    );
    return r.rows.map((row) => row.name);
  } finally {
    await c.end();
  }
}

// adminKeyFor reads the admin key for the given deployment row. The probe
// in adopt_deployment requires the real key — random hex won't pass
// /api/check_admin_key against a Convex backend.
async function adminKeyFor(name: string): Promise<string> {
  const c = new Client({ connectionString: DB_URL });
  await c.connect();
  try {
    const r = await c.query<{ admin_key: string }>(
      `SELECT admin_key FROM deployments WHERE name = $1`,
      [name],
    );
    if (!r.rows[0]) throw new Error(`deployment ${name} not found`);
    return r.rows[0].admin_key;
  } finally {
    await c.end();
  }
}

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

test.beforeEach(async () => {
  await truncateAll();
  pruneSynapseContainers();
});

test.afterEach(async () => {
  pruneSynapseContainers();
});

test("adopt an existing Convex backend via the dashboard", async ({ page }) => {
  await setupProject(page);

  // Provision a real container we can re-use as the "external" backend
  // for the adopt flow. The probe inside synapse hits its docker-network
  // DNS (convex-<name>:3210), not the host port — synapse-api running in
  // a container can't reach 127.0.0.1:3210 (that's its own loopback).
  await page.getByRole("button", { name: /create deployment/i }).first().click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create", exact: true })
    .click();
  const provisionedName = (
    await page
      .getByText(/-[a-z]+-\d{4}/)
      .first()
      .textContent({ timeout: 90_000 })
  )?.trim();
  if (!provisionedName) throw new Error("could not read provisioned name");

  // Wait for the deployment to flip from 'provisioning' to 'running' —
  // the adopt probe hits /version, which only responds after the
  // container is up. The status badge appears next to the name; the
  // page polls every 2s.
  await expect(
    page
      .getByText(provisionedName, { exact: true })
      .locator("..")
      .getByText("running"),
  ).toBeVisible({ timeout: 60_000 });

  // Open the adopt dialog.
  await page.getByRole("button", { name: "Adopt existing deployment" }).click();
  const adoptDialog = page.getByRole("dialog");
  await expect(adoptDialog).toBeVisible();

  // Use the same backend's admin key + its in-network URL. Reflexive but
  // works: it proves the probe + persistence path end-to-end without
  // standing up a separate backend just for this test.
  const adminKey = await adminKeyFor(provisionedName);
  await adoptDialog
    .locator("#adopt-url")
    .fill(`http://convex-${provisionedName}:3210`);
  await adoptDialog.locator("#adopt-admin-key").fill(adminKey);
  await adoptDialog.locator("#adopt-name").fill("imported-app");
  await adoptDialog.getByRole("button", { name: "Adopt", exact: true }).click();

  // Adopt button should disappear once the dialog closes; the new row shows
  // up in the deployments list with the "adopted" badge.
  await expect(adoptDialog).toBeHidden({ timeout: 30_000 });
  await expect(
    page.getByText("imported-app", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("listitem").filter({ hasText: "imported-app" }).getByText("adopted").first(),
  ).toBeVisible({ timeout: 5_000 }).catch(async () => {
    // Fallback for renderings that don't wrap rows in <li>: just look
    // anywhere on the page near the row.
    await expect(page.getByText("adopted").first()).toBeVisible();
  });

  // Confirm DB sees adopted=true.
  await expect.poll(adoptedNames, { timeout: 5_000 }).toContain("imported-app");

  // The provisioned container is still the only managed container —
  // adopting did NOT spin up a new one.
  expect(listSynapseContainerNames()).toEqual([`convex-${provisionedName}`]);

  // Deleting the adopted row must NOT remove the container — it isn't ours.
  page.on("dialog", (d) => d.accept());
  await page
    .getByRole("button", { name: /delete deployment imported-app/i })
    .click();
  await expect(
    page.getByText("imported-app", { exact: true }),
  ).toBeHidden({ timeout: 10_000 });

  // Container is still alive (the original provisioned one).
  expect(listSynapseContainerNames()).toEqual([`convex-${provisionedName}`]);
});

test("adopt with a wrong admin key surfaces the server error", async ({
  page,
}) => {
  await setupProject(page);

  // Provision a backend so we have a real URL to point at.
  await page.getByRole("button", { name: /create deployment/i }).first().click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create", exact: true })
    .click();
  const provisionedName = (
    await page
      .getByText(/-[a-z]+-\d{4}/)
      .first()
      .textContent({ timeout: 90_000 })
  )?.trim();
  if (!provisionedName) throw new Error("could not read provisioned name");

  // Wait until the container responds /version before trying adopt —
  // otherwise the probe fails with probe_failed (URL unreachable)
  // instead of the invalid_admin_key path we want to assert.
  await expect(
    page
      .getByText(provisionedName, { exact: true })
      .locator("..")
      .getByText("running"),
  ).toBeVisible({ timeout: 60_000 });

  await page.getByRole("button", { name: "Adopt existing deployment" }).click();
  const dialog = page.getByRole("dialog");
  await dialog
    .locator("#adopt-url")
    .fill(`http://convex-${provisionedName}:3210`);
  await dialog.locator("#adopt-admin-key").fill("definitely-wrong-key");
  await dialog.getByRole("button", { name: "Adopt", exact: true }).click();

  // Form should stay open with the server-supplied error.
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText(/rejected/i)).toBeVisible({ timeout: 10_000 });
});
