import { test, expect, type Page } from "@playwright/test";
import { truncateAll } from "./helpers/db";

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

test("edit profile name from /me", async ({ page }) => {
  await registerViaUI(page);

  await page.getByRole("link", { name: "Account" }).click();
  await expect(page).toHaveURL(/\/me\b/);
  await expect(page.getByTestId("me-name")).toHaveText("Ian");

  await page.getByRole("button", { name: "Edit profile name" }).click();
  await page.locator("#me-name-input").fill("Renamed Ian");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByTestId("me-name")).toHaveText("Renamed Ian");
});

test("delete account is guarded while user owns a team, succeeds after cleanup", async ({
  page,
}) => {
  await registerViaUI(page);

  // Create a team so the user is BOTH last admin AND creator. The backend
  // checks last_admin first; surface that hint and confirm the dialog stays
  // open. After cleanup the same call succeeds.
  await page.getByRole("button", { name: "Create team" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("Solo Co");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("link", { name: /solo co/i })).toBeVisible();

  await page.getByRole("link", { name: "Account" }).click();
  await page.getByRole("button", { name: "Delete account" }).click();
  let deleteDialog = page.getByRole("dialog");
  await deleteDialog.locator("#me-delete-confirm").fill("ian@example.com");
  await deleteDialog
    .getByRole("button", { name: "Delete account", exact: true })
    .click();

  // Either the last_admin or team_creator hint is acceptable — both are
  // valid 409 codes the backend can return. We assert via the union so
  // a server-side check-order swap doesn't break the test.
  await expect(
    deleteDialog.getByText(
      /(last admin of one or more teams|created one or more teams)/i,
    ),
  ).toBeVisible();
  await deleteDialog.getByRole("button", { name: "Cancel" }).click();

  // Delete the team via Settings → General → Delete team.
  await page.goto("/teams/solo-co/settings/general");
  await page.getByTestId("team-delete-open").click();
  const teamDialog = page.getByRole("dialog");
  await teamDialog.locator("#team-delete-confirm").fill("Solo Co");
  await teamDialog.getByTestId("team-delete-confirm").click();
  await expect(page).toHaveURL(/\/teams$/);

  // Now delete_account succeeds and bounces to /login.
  await page.getByRole("link", { name: "Account" }).click();
  await page.getByRole("button", { name: "Delete account" }).click();
  deleteDialog = page.getByRole("dialog");
  await deleteDialog.locator("#me-delete-confirm").fill("ian@example.com");
  await deleteDialog
    .getByRole("button", { name: "Delete account", exact: true })
    .click();
  // After the last admin's account goes, install_status flips to firstRun=true
  // and /login redirects to /setup. Either is acceptable — we just want the
  // user signed out and off any authenticated page.
  await expect(page).toHaveURL(/\/(login|setup)\b/);
});
