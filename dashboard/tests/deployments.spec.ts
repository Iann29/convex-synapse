import { test, expect, type Page } from "@playwright/test";
import { truncateAll } from "./helpers/db";
import {
  listSynapseContainerNames,
  pruneSynapseContainers,
} from "./helpers/docker";

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

  // Project URL uses the project UUID (the dashboard renders the slug
  // inside the card but routes by id for stability across renames).
  await expect(page).toHaveURL(/\/teams\/amage\/[0-9a-f-]{36}\b/);
}

test.beforeEach(async () => {
  await truncateAll();
  pruneSynapseContainers();
});

test.afterEach(async () => {
  pruneSynapseContainers();
});

test("provision a deployment via the dashboard", async ({ page }) => {
  await setupProject(page);

  // Empty-state CTA on the project page.
  await page.getByRole("button", { name: /create deployment/i }).first().click();
  const dialog = page.getByRole("dialog");
  // Default type is "dev" — submit straight away.
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  // Wait for the deployment row — the friendly name follows
  // "<adj>-<animal>-<NNNN>" so a 4-digit-suffix anchor is reliable.
  await expect(page.getByText(/-[a-z]+-\d{4}/).first()).toBeVisible({
    timeout: 90_000,
  });

  // Then poll Docker (it's the source of truth) — the dashboard's row may
  // appear as "provisioning" before "running" depending on SWR timing.
  await expect.poll(() => listSynapseContainerNames().length, {
    timeout: 30_000,
    intervals: [500, 1000, 2000],
  }).toBeGreaterThan(0);

  const containers = listSynapseContainerNames();
  expect(containers[0]).toMatch(/^convex-/);
});
