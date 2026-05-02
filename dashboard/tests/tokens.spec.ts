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

test("create a personal access token, see it once, then delete it", async ({
  page,
}) => {
  await registerViaUI(page);

  // Header email links to /me — exercise that path rather than typing the URL.
  await page.getByRole("link", { name: "Account" }).click();
  await expect(page).toHaveURL(/\/me\b/);

  // Open the create-token dialog from the panel header. The TokensPanel
  // component scopes the input id by token scope (`token-name-user` here)
  // so multiple panels on a page don't collide on the id.
  await page.getByRole("button", { name: "New token" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.locator("#token-name-user").fill("ci-runner");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  // Plaintext token shows up exactly once; must be a syn_* string.
  const issued = dialog.getByTestId("issued-token");
  await expect(issued).toBeVisible();
  const tokenText = (await issued.textContent()) ?? "";
  expect(tokenText.trim()).toMatch(/^syn_/);

  // Close the dialog — the row should now be visible in the list.
  await dialog.getByRole("button", { name: "Done" }).click();
  const row = page.getByTestId("token-row-ci-runner");
  await expect(row).toBeVisible();
  await expect(row.getByText("ci-runner")).toBeVisible();

  // Native confirm() wraps Delete; accept it, then verify the row vanishes.
  page.on("dialog", (d) => d.accept());
  await row.getByRole("button", { name: /delete token ci-runner/i }).click();
  await expect(page.getByTestId("token-row-ci-runner")).toBeHidden();
  await expect(page.getByText("No tokens yet.")).toBeVisible();
});
