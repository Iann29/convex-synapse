import { test, expect, type Page } from "@playwright/test";
import { truncateAll } from "./helpers/db";

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
});

test("project env vars: add, list, delete", async ({ page }) => {
  await setupProject(page);

  // Initially empty.
  await expect(page.getByText("No env vars yet.")).toBeVisible();

  // Add one.
  await page.locator("#envvar-name").fill("API_KEY");
  await page.locator("#envvar-value").fill("supersecret");
  await page.getByRole("button", { name: "Add" }).click();

  await expect(page.getByText("API_KEY")).toBeVisible();
  await expect(page.getByText("supersecret")).toBeVisible();

  // Add a second one to confirm the list grows.
  await page.locator("#envvar-name").fill("DATABASE_URL");
  await page.locator("#envvar-value").fill("postgres://db");
  await page.getByRole("button", { name: "Add" }).click();
  await expect(page.getByText("DATABASE_URL")).toBeVisible();

  // Delete API_KEY.
  await page.getByRole("button", { name: "Delete env var API_KEY" }).click();
  await expect(page.getByText("API_KEY")).toBeHidden();
  // The other one survives.
  await expect(page.getByText("DATABASE_URL")).toBeVisible();
});
