import { test, expect } from "@playwright/test";
import { truncateAll } from "./helpers/db";

test.beforeEach(async () => {
  await truncateAll();
});

test("register → lands on /teams empty state", async ({ page }) => {
  await page.goto("/register");
  await page.locator("#register-email").fill("ian@example.com");
  await page.locator("#register-password").fill("strongpass123");
  await page.locator("#register-name").fill("Ian");
  await page.getByRole("button", { name: "Create account" }).click();

  await expect(page).toHaveURL(/\/teams\b/);
  // Empty state copy.
  await expect(page.getByText("No teams yet.")).toBeVisible();
});

test("login with wrong password shows error and stays on /login", async ({ page }) => {
  // First register a real user via UI so the email exists.
  await page.goto("/register");
  await page.locator("#register-email").fill("ian@example.com");
  await page.locator("#register-password").fill("strongpass123");
  await page.locator("#register-name").fill("Ian");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL(/\/teams\b/);

  // Sign out by clearing storage.
  await page.evaluate(() => localStorage.clear());
  await page.goto("/login");

  await page.locator("#login-email").fill("ian@example.com");
  await page.locator("#login-password").fill("WRONG-password");
  await page.getByRole("button", { name: "Sign in" }).click();

  await expect(page).toHaveURL(/\/login\b/);
  // Some error indicator must appear — exact wording depends on the API.
  await expect(page.getByRole("alert")).toBeVisible();
});

test("anonymous /teams redirects to /login", async ({ page }) => {
  await page.goto("/teams");
  await expect(page).toHaveURL(/\/login\b/);
});
