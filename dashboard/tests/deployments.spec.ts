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

  await page.getByRole("button", { name: /create deployment/i }).first().click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  // Wait for the deployment row — friendly name "<adj>-<animal>-<NNNN>".
  await expect(page.getByText(/-[a-z]+-\d{4}/).first()).toBeVisible({
    timeout: 90_000,
  });

  await expect
    .poll(() => listSynapseContainerNames().length, {
      timeout: 30_000,
      intervals: [500, 1000, 2000],
    })
    .toBeGreaterThan(0);

  const containers = listSynapseContainerNames();
  expect(containers[0]).toMatch(/^convex-/);
});

test("delete a deployment via the dashboard", async ({ page }) => {
  await setupProject(page);

  // Provision first.
  await page.getByRole("button", { name: /create deployment/i }).first().click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create", exact: true })
    .click();

  // Capture the deployment name from the rendered row.
  const nameLocator = page.getByText(/-[a-z]+-\d{4}/).first();
  await expect(nameLocator).toBeVisible({ timeout: 90_000 });
  const deploymentName = (await nameLocator.textContent())?.trim() ?? "";
  expect(deploymentName).toMatch(/^[a-z]+-[a-z]+-\d{4}$/);

  // Auto-accept the native confirm() that the delete handler raises.
  page.on("dialog", (d) => d.accept());

  await page
    .getByRole("button", { name: new RegExp(`delete deployment ${deploymentName}`, "i") })
    .click();

  // Row disappears from the list.
  await expect(nameLocator).toBeHidden({ timeout: 15_000 });

  // Container is gone on the host too.
  await expect
    .poll(() => listSynapseContainerNames(), {
      timeout: 15_000,
    })
    .toEqual([]);
});
