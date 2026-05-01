import { test, expect } from "@playwright/test";
import { truncateAll } from "./helpers/db";

// First-run wizard (v0.6.3). Reachable only when /v1/install_status
// reports firstRun=true, i.e. the users table is empty. Verifies:
//
//   - /login redirects to /setup on a fresh DB
//   - /setup creates an admin and lands on the demo step
//   - the demo step bootstraps a team + project + dev deployment and
//     drops the operator on the project page with the deployment row
//     present
//   - /setup on a non-fresh DB redirects back to /login

test.beforeEach(async () => {
  await truncateAll();
});

test("/login redirects to /setup when the DB is empty", async ({ page }) => {
  await page.goto("/login");
  await expect(page).toHaveURL(/\/setup\b/);
  // The wizard heading is the canonical signal we made it through.
  await expect(page.getByText(/Welcome to Synapse/i)).toBeVisible();
});

test("wizard end-to-end: admin → demo team + project + deployment", async ({ page }) => {
  await page.goto("/setup");

  // Step 1 — admin
  await page.locator("#setup-email").fill("ian@example.com");
  await page.locator("#setup-password").fill("strongpass123");
  await page.locator("#setup-name").fill("Ian");
  await page.locator("#setup-admin-submit").click();

  // Step 2 — demo (defaults are "Default" / "demo")
  await expect(page.getByText(/Step 2/)).toBeVisible();
  await page.locator("#setup-demo-submit").click();

  // The deployment provisioner is async (~1s). The wizard's
  // "All set!" pane appears once the API call returns 201.
  await expect(page.getByText(/All set!/i)).toBeVisible({ timeout: 30_000 });
  await page.locator("#setup-finish").click();

  // The project page renders the freshly-provisioned deployment row.
  await expect(page).toHaveURL(/\/teams\/[^/]+\/[^/]+\b/);
  // CLI credentials block is the give-away that a deployment is on the page.
  await expect(page.getByText(/CONVEX_SELF_HOSTED_URL/i)).toBeVisible({ timeout: 30_000 });
});

test("/setup redirects to /login when an admin already exists", async ({ page }) => {
  // Register a user via the regular /register page — this populates
  // the users table, flipping firstRun to false.
  await page.goto("/register");
  await page.locator("#register-email").fill("ian@example.com");
  await page.locator("#register-password").fill("strongpass123");
  await page.locator("#register-name").fill("Ian");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL(/\/teams\b/);
  await page.evaluate(() => localStorage.clear());

  // Now the wizard shouldn't accept us.
  await page.goto("/setup");
  await expect(page).toHaveURL(/\/login\b/, { timeout: 10_000 });
});

test("wizard skip-demo button drops you on /teams", async ({ page }) => {
  await page.goto("/setup");
  await page.locator("#setup-email").fill("ian@example.com");
  await page.locator("#setup-password").fill("strongpass123");
  await page.locator("#setup-admin-submit").click();
  await expect(page.getByText(/Step 2/)).toBeVisible();
  await page.locator("#setup-demo-skip").click();
  await expect(page).toHaveURL(/\/teams\b/);
});
