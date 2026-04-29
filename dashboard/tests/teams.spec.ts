import { test, expect, type Page } from "@playwright/test";
import { truncateAll } from "./helpers/db";

async function registerViaUI(page: Page) {
  await page.goto("/register");
  await page.locator("#register-email").fill("ian@example.com");
  await page.locator("#register-password").fill("strongpass123");
  await page.locator("#register-name").fill("Ian");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL(/\/teams\b/);
}

test.beforeEach(async () => {
  await truncateAll();
});

test("create a team from the empty state", async ({ page }) => {
  await registerViaUI(page);

  await page.getByRole("button", { name: "Create team" }).click();
  // Submit lives inside the dialog — scope the click so it doesn't match the
  // empty-state "Create team" button still rendered behind the modal.
  const dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("Amage Web");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  await expect(page.getByRole("link", { name: /amage web/i })).toBeVisible();
});

test("create team then create project", async ({ page }) => {
  await registerViaUI(page);

  await page.getByRole("button", { name: "Create team" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("Amage");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  await page.getByRole("link", { name: /amage/i }).click();
  await expect(page).toHaveURL(/\/teams\/amage\b/);

  await page.getByRole("button", { name: "Create project" }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#project-name").fill("My Store");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  await expect(page.getByRole("link", { name: /my store/i })).toBeVisible();
});

test("delete project from its detail page", async ({ page }) => {
  await registerViaUI(page);

  // Create team + project.
  await page.getByRole("button", { name: "Create team" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("Amage");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: /amage/i }).click();

  await page.getByRole("button", { name: "Create project" }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#project-name").fill("Trash Me");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  // Enter the project, click Delete project, accept confirm.
  await page.getByRole("link", { name: /trash me/i }).click();
  await expect(page).toHaveURL(/\/teams\/amage\/[0-9a-f-]{36}\b/);

  page.on("dialog", (d) => d.accept());
  await page.getByRole("button", { name: "Delete project" }).click();

  // Bounced back to the team page; the project no longer appears.
  await expect(page).toHaveURL(/\/teams\/amage\b/);
  await expect(page.getByRole("link", { name: /trash me/i })).toBeHidden();
});
