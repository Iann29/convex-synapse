import { test, expect, type Page } from "@playwright/test";
import { truncateAll } from "./helpers/db";
import { pruneSynapseContainers } from "./helpers/docker";

// CLI credentials panel — exercises components/CliCredentialsPanel.tsx end to
// end: register → team → project → deployment → reveal credentials → copy →
// hide. The reveal/copy buttons aria-labels are scoped per deployment so the
// test reuses regex-matchers like the existing deployments.spec.

const SYNAPSE_URL =
  process.env.NEXT_PUBLIC_SYNAPSE_URL?.replace(/\/$/, "") ||
  "http://localhost:8080";

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

// Same shape as proxy.spec — pulls the dashboard's JWT so we can compare
// the panel's URL against what the auth endpoint actually returns.
async function bearerFromPage(page: Page): Promise<string> {
  const token = await page.evaluate(() => {
    const raw = localStorage.getItem("synapse.auth");
    if (!raw) return "";
    try {
      const j = JSON.parse(raw) as { accessToken?: string };
      return j.accessToken ?? "";
    } catch {
      return "";
    }
  });
  if (!token) throw new Error("could not extract bearer token from page");
  return token;
}

test.beforeEach(async () => {
  await truncateAll();
  pruneSynapseContainers();
});

test.afterEach(async () => {
  pruneSynapseContainers();
});

test("CLI credentials panel: reveal, copy to clipboard, hide", async ({
  page,
  context,
  request,
}) => {
  // Chromium gates clipboard.readText() behind explicit permission grants.
  // Without these, navigator.clipboard.readText() rejects with NotAllowedError
  // and the assertion below would falsely fail in CI.
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);

  await setupProject(page);

  await page.getByRole("button", { name: /create deployment/i }).first().click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create", exact: true })
    .click();

  const nameLocator = page.getByText(/-[a-z]+-\d{4}/).first();
  await expect(nameLocator).toBeVisible({ timeout: 90_000 });
  const deploymentName = (await nameLocator.textContent())?.trim() ?? "";
  expect(deploymentName).toMatch(/^[a-z]+-[a-z]+-\d{4}$/);

  // Reveal: aria-label is "Show CLI credentials for {name}", set in the
  // panel's hidden state. After click, the snippet block should appear.
  const showButton = page.getByRole("button", {
    name: `Show CLI credentials for ${deploymentName}`,
  });
  await expect(showButton).toBeVisible();
  await showButton.click();

  // The expanded panel renders a <pre> with the export snippet. Scope our
  // checks to that block to avoid colliding with the env-vars panel below.
  const snippet = page.locator("pre").filter({ hasText: "CONVEX_SELF_HOSTED_URL=" });
  await expect(snippet).toBeVisible({ timeout: 15_000 });
  const snippetText = (await snippet.textContent())?.trim() ?? "";
  expect(snippetText).toContain("CONVEX_SELF_HOSTED_URL=");
  expect(snippetText).toContain("CONVEX_SELF_HOSTED_ADMIN_KEY=");

  // The URL inside the snippet should match what the auth endpoint reports.
  // The handler shell-quotes single-tickless values, so the URL appears as
  // 'http://127.0.0.1:NNNN' (single quotes) — strip those before matching.
  const bearer = await bearerFromPage(page);
  const authResp = await request.get(
    `${SYNAPSE_URL}/v1/deployments/${encodeURIComponent(deploymentName)}/auth`,
    { headers: { Authorization: `Bearer ${bearer}` } },
  );
  expect(authResp.ok()).toBeTruthy();
  const auth = (await authResp.json()) as { deploymentUrl: string };
  expect(snippetText).toContain(auth.deploymentUrl);

  // Copy → clipboard.readText() should round-trip the same snippet. The
  // panel swaps the button's visible text to "Copied!" for ~1.5s; the
  // aria-label is fixed, so we wait on the text instead. (Accessible name
  // is dominated by aria-label here, so getByRole(name:"Copied!") never
  // matches — match the inner text node directly.)
  const copyButton = page.getByRole("button", {
    name: "Copy CLI credentials snippet",
  });
  await copyButton.click();
  await expect(copyButton).toHaveText("Copied!", { timeout: 5_000 });
  const clipboardText = await page.evaluate(() =>
    navigator.clipboard.readText(),
  );
  expect(clipboardText).toBe(snippetText);

  // Hide: the snippet collapses, the show button comes back. The panel's
  // re-render keys off of `creds` going null — the pre block should detach
  // from the DOM, not just visually hide.
  await page.getByRole("button", { name: "Hide CLI credentials" }).click();
  await expect(snippet).toBeHidden();
  await expect(
    page.getByRole("button", {
      name: `Show CLI credentials for ${deploymentName}`,
    }),
  ).toBeVisible();
});
