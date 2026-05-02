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

test("team-scoped token: create + reveal + list + delete", async ({ page }) => {
  await registerViaUI(page);
  await page.getByRole("button", { name: "Create team" }).click();
  const d = page.getByRole("dialog");
  await d.locator("#team-name").fill("Tok Co");
  await d.getByRole("button", { name: "Create", exact: true }).click();

  await page.goto("/teams/tok-co/settings/access-tokens");
  await page.getByTestId("tokens-new-team").click();
  const dlg = page.getByRole("dialog");
  await dlg.locator("#token-name-team").fill("ci-runner");
  await page.getByTestId("tokens-create-team").click();

  // Plaintext shown once.
  const issued = dlg.getByTestId("issued-token");
  await expect(issued).toBeVisible();
  const tokenText = (await issued.textContent()) ?? "";
  expect(tokenText.trim()).toMatch(/^syn_/);

  // Close + see row in list.
  await dlg.getByRole("button", { name: "Done" }).click();
  const row = page.getByTestId("token-row-ci-runner");
  await expect(row).toBeVisible();
  // Scope label reflects team binding.
  await expect(row.getByText(/scope: team/i)).toBeVisible();

  // Delete and confirm the row vanishes.
  page.on("dialog", (dlg) => dlg.accept());
  await row.getByRole("button", { name: /delete token ci-runner/i }).click();
  await expect(page.getByTestId("token-row-ci-runner")).toBeHidden();
});

test("project page renders project + app token panels separately", async ({
  page,
}) => {
  await registerViaUI(page);
  await page.getByRole("button", { name: "Create team" }).click();
  let d = page.getByRole("dialog");
  await d.locator("#team-name").fill("Tok Co");
  await d.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: /tok co/i }).click();

  await page.getByRole("button", { name: "Create project" }).click();
  d = page.getByRole("dialog");
  await d.locator("#project-name").fill("Tok Project");
  await d.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: /tok project/i }).click();

  // Two distinct New-token buttons — project + app — must be present.
  await expect(page.getByTestId("tokens-new-project")).toBeVisible();
  await expect(page.getByTestId("tokens-new-app")).toBeVisible();

  // Create a project-scoped token.
  await page.getByTestId("tokens-new-project").click();
  let dlg = page.getByRole("dialog");
  await dlg.locator("#token-name-project").fill("project-key");
  await page.getByTestId("tokens-create-project").click();
  await dlg.getByRole("button", { name: "Done" }).click();

  // App-scoped token.
  await page.getByTestId("tokens-new-app").click();
  dlg = page.getByRole("dialog");
  await dlg.locator("#token-name-app").fill("preview-key");
  await page.getByTestId("tokens-create-app").click();
  await dlg.getByRole("button", { name: "Done" }).click();

  // Each shows up only in its own panel — pin both rows visible.
  await expect(page.getByTestId("token-row-project-key")).toBeVisible();
  await expect(page.getByTestId("token-row-preview-key")).toBeVisible();
});

test("project transfer moves the project to another team", async ({ page }) => {
  await registerViaUI(page);
  // Source team via empty-state "Create team".
  await page.getByRole("button", { name: "Create team" }).click();
  let d = page.getByRole("dialog");
  await d.locator("#team-name").fill("Source Co");
  await d.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("link", { name: /source co/i })).toBeVisible();

  // Dest team via the populated header — empty-state button is gone now,
  // the populated layout exposes "New team" instead.
  await page.getByRole("button", { name: "New team" }).click();
  d = page.getByRole("dialog");
  await d.locator("#team-name").fill("Dest Co");
  await d.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("link", { name: /dest co/i })).toBeVisible();

  // Project lives in source team.
  await page.getByRole("link", { name: /source co/i }).click();
  await page.getByRole("button", { name: "Create project" }).click();
  d = page.getByRole("dialog");
  await d.locator("#project-name").fill("Movable");
  await d.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: /movable/i }).click();

  // Open transfer dialog, pick dest team, submit.
  await page.getByTestId("project-transfer-open").click();
  const dlg = page.getByRole("dialog");
  // selectOption accepts a substring label — match the option text directly.
  await dlg.locator("#transfer-dest").selectOption({ label: "Dest Co (dest-co)" });
  await page.getByTestId("project-transfer-submit").click();

  // URL now points at /teams/dest-co/<projectId>.
  await expect(page).toHaveURL(/\/teams\/dest-co\/[0-9a-f-]{36}\b/);
  // Dest team's project list shows the moved project.
  await page.goto("/teams/dest-co");
  await expect(page.getByRole("link", { name: /movable/i })).toBeVisible();
});
