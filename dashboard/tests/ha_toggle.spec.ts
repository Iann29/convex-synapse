import { test, expect, type Page } from "@playwright/test";
import { truncateAll } from "./helpers/db";
import { pruneSynapseContainers } from "./helpers/docker";

// HA toggle wiring. The Synapse instance the compose stack runs is
// SYNAPSE_HA_ENABLED=false by default, so submitting `ha:true` returns
// 400 ha_disabled. The test asserts the toggle is wired (exists, is
// interactive) and that the server's error surfaces inline in the
// dialog instead of crashing it.
//
// Real HA-mode end-to-end (replicas come up against a live Postgres +
// MinIO) is out of scope here — covered by Go integration tests
// (ha_e2e_test.go) plus the gated SYNAPSE_HA_E2E=1 real-backend job.
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

test("HA toggle: hint appears when checked, server error surfaces inline on a non-HA cluster", async ({
  page,
}) => {
  await setupProject(page);

  await page.getByRole("button", { name: /create deployment/i }).first().click();
  const dialog = page.getByRole("dialog");

  // Toggle exists, off by default, hint is hidden until checked.
  const toggle = dialog.locator("#create-ha-toggle");
  await expect(toggle).toBeVisible();
  await expect(toggle).not.toBeChecked();
  await expect(dialog.getByText(/SYNAPSE_HA_ENABLED=true/)).toBeHidden();

  // Check the box → hint reveals.
  await toggle.check();
  await expect(toggle).toBeChecked();
  await expect(dialog.getByText(/SYNAPSE_HA_ENABLED=true/)).toBeVisible();

  // Submit with HA on against a non-HA cluster → backend returns 400
  // ha_disabled; the dialog stays open with the message inline.
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText(/HA-per-deployment is disabled/i)).toBeVisible({
    timeout: 5_000,
  });
});

test("HA toggle: provisioning a single-replica deployment still works when HA is unchecked", async ({
  page,
}) => {
  await setupProject(page);

  await page.getByRole("button", { name: /create deployment/i }).first().click();
  const dialog = page.getByRole("dialog");
  // Don't touch the HA toggle — submit a regular deployment.
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  // Same assertion as the legacy deployments spec: a row with the
  // friendly-name pattern lands in the list.
  await expect(page.getByText(/-[a-z]+-\d{4}/).first()).toBeVisible({
    timeout: 90_000,
  });
});
