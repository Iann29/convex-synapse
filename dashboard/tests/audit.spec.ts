import { test, expect, type Page } from "@playwright/test";
import { truncateAll } from "./helpers/db";

// Mirrors the helper used in teams.spec.ts. Inlined here to keep this spec
// self-contained — the existing helper lives inside teams.spec.ts and isn't
// exported. Kept identical otherwise.
async function registerViaUI(page: Page, email = "ian@example.com") {
  await page.goto("/register");
  await page.locator("#register-email").fill(email);
  await page.locator("#register-password").fill("strongpass123");
  await page.locator("#register-name").fill("Ian");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL(/\/teams\b/);
}

test.beforeEach(async () => {
  await truncateAll();
});

test("audit log records team creation and project creation", async ({
  page,
}) => {
  const email = "ian@example.com";
  await registerViaUI(page, email);

  // Create a team via the UI so the createTeam audit event lands with the
  // correct actor → user mapping.
  await page.getByRole("button", { name: "Create team" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("Audit Co");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  await page.getByRole("link", { name: /audit co/i }).click();
  await expect(page).toHaveURL(/\/teams\/audit-co\b/);

  // Create a project so we have a second event to assert on.
  await page.getByRole("button", { name: "Create project" }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#project-name").fill("Logger");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("link", { name: /logger/i })).toBeVisible();

  // Navigate into the audit log.
  await page.getByRole("link", { name: /audit log/i }).click();
  await expect(page).toHaveURL(/\/teams\/audit-co\/audit\b/);

  // The page renders a table with one row per audit event. We assert on the
  // *rows* (data-testid) so the layout can change without breaking the spec.
  const rows = page.getByTestId("audit-log-row");
  // Both events must show up; allow the polling loop a moment to refresh.
  await expect(rows).toHaveCount(2, { timeout: 5_000 });

  const tableText = (await page.getByTestId("audit-log-table").textContent()) ?? "";
  expect(tableText).toContain("createTeam");
  expect(tableText).toContain("createProject");
  expect(tableText).toContain(email);
});
