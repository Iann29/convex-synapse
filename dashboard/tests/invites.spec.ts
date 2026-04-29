import { test, expect, type Page } from "@playwright/test";
import { truncateAll } from "./helpers/db";

async function registerAs(page: Page, email: string, name: string) {
  await page.goto("/register");
  await page.locator("#register-email").fill(email);
  await page.locator("#register-password").fill("strongpass123");
  await page.locator("#register-name").fill(name);
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL(/\/teams\b/);
}

test.beforeEach(async () => {
  await truncateAll();
});

test("issue invite, accept it as another user, both see the team", async ({
  browser,
}) => {
  // Admin context
  const adminCtx = await browser.newContext();
  const admin = await adminCtx.newPage();

  await registerAs(admin, "owner@example.com", "Owner");
  await admin.getByRole("button", { name: "Create team" }).click();
  let dialog = admin.getByRole("dialog");
  await dialog.locator("#team-name").fill("Crew");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await admin.getByRole("link", { name: /crew/i }).click();
  await expect(admin).toHaveURL(/\/teams\/crew\b/);

  // Issue invite from the InvitesPanel.
  await admin.locator("#invite-email").fill("guest@example.com");
  await admin.getByRole("button", { name: "Invite" }).click();

  // The "share this URL" card appears with /accept-invite?token=…
  const inviteUrlBlock = admin
    .locator("text=/accept-invite\\?token=[\\w-]+/")
    .first();
  await expect(inviteUrlBlock).toBeVisible({ timeout: 5_000 });
  const inviteLine = (await inviteUrlBlock.textContent()) ?? "";
  const tokenMatch = inviteLine.match(/token=([\w-]+)/);
  expect(tokenMatch).not.toBeNull();
  const token = tokenMatch![1];

  // The pending invite is listed too — there's also a "share this URL"
  // card that mentions the email, so just confirm at least one is visible.
  await expect(admin.getByText("guest@example.com").first()).toBeVisible();

  // Invitee context — fresh browser session, register, then accept.
  const guestCtx = await browser.newContext();
  const guest = await guestCtx.newPage();
  await registerAs(guest, "guest@example.com", "Guest");

  // Hit the accept-invite URL with the token.
  await guest.goto(`/accept-invite?token=${token}`);
  await guest.getByRole("button", { name: "Accept invite" }).click();
  await expect(guest.getByText(/now a member of/i)).toBeVisible();
  await guest.getByRole("button", { name: "Go to team" }).click();
  await expect(guest).toHaveURL(/\/teams\/crew\b/);

  // Admin's invites panel no longer shows the pending invite.
  // The "issued" card may still hold a stale email reference until reload —
  // after reload, no copy of the email should remain anywhere in the panel.
  await admin.reload();
  await expect(admin.getByText("No pending invites.")).toBeVisible();

  // Both see the team in their list.
  await admin.goto("/teams");
  await expect(admin.getByRole("link", { name: /crew/i })).toBeVisible();
  await guest.goto("/teams");
  await expect(guest.getByRole("link", { name: /crew/i })).toBeVisible();

  await adminCtx.close();
  await guestCtx.close();
});
