import { test, expect, type Page, type Route } from "@playwright/test";
import { truncateAll } from "./helpers/db";

async function registerAdmin(page: Page) {
  await page.goto("/register");
  await page.locator("#register-email").fill("admin@example.com");
  await page.locator("#register-password").fill("strongpass123");
  await page.locator("#register-name").fill("Instance Admin");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL(/\/teams\b/);
}

async function mockUpdateAvailable(page: Page): Promise<void> {
  await page.route("**/v1/admin/version_check", async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        current: "1.1.0",
        latest: "1.2.0",
        updateAvailable: true,
        releaseNotes: "Keep upgrade progress visible after closing the modal.",
        fetchedAt: "2026-05-11T12:00:00Z",
      }),
    });
  });
}

async function mockUpgradeFlow(page: Page): Promise<void> {
  let started = false;

  await page.route("**/v1/admin/upgrade/status", async (route: Route) => {
    if (route.request().method() !== "GET") {
      await route.continue();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(
        started
          ? {
              state: "running",
              ref: "main",
              startedAt: "2026-05-11T12:01:00Z",
              logTail: ["Installing Synapse 1.2.0", "Restarting containers"],
            }
          : { state: "idle" },
      ),
    });
  });

  await page.route("**/v1/admin/upgrade", async (route: Route) => {
    if (route.request().method() !== "POST") {
      await route.continue();
      return;
    }
    started = true;
    await route.fulfill({
      status: 202,
      contentType: "application/json",
      body: JSON.stringify({ started: true, ref: "main" }),
    });
  });
}

async function mockIdleUpgradeStatus(page: Page): Promise<void> {
  await page.route("**/v1/admin/upgrade/status", async (route: Route) => {
    if (route.request().method() !== "GET") {
      await route.continue();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ state: "idle" }),
    });
  });
}

test.beforeEach(async () => {
  await truncateAll();
});

test("upgrade progress is restored after closing and reopening the modal", async ({
  page,
}) => {
  await mockUpdateAvailable(page);
  await mockUpgradeFlow(page);
  await registerAdmin(page);

  await page.getByRole("button", { name: "Review & upgrade" }).click();
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByRole("button", { name: "Upgrade now" }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Upgrading Synapse");
  await expect(dialog).toContainText("Installing Synapse 1.2.0");

  await page.locator("div.fixed.inset-0").click({ position: { x: 5, y: 5 } });
  await expect(page.getByRole("dialog")).toBeHidden();

  await page.getByRole("button", { name: "Review & upgrade" }).click();

  await expect(page.getByRole("dialog")).toContainText("Upgrading Synapse");
  await expect(page.getByRole("dialog")).toContainText("Installing Synapse 1.2.0");
});

test("stale upgrade marker returns to review when updater is idle", async ({
  page,
}) => {
  await mockUpdateAvailable(page);
  await mockIdleUpgradeStatus(page);
  await registerAdmin(page);
  await page.evaluate(() => {
    window.sessionStorage.setItem("synapse-upgrade-in-progress", "1");
  });

  await page.getByRole("button", { name: "Review & upgrade" }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Upgrade to Synapse v1.2.0");
  await expect
    .poll(() =>
      page.evaluate(() =>
        window.sessionStorage.getItem("synapse-upgrade-in-progress"),
      ),
    )
    .toBeNull();
});
