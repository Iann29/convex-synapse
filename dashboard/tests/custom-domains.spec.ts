import { test, expect, type Page } from "@playwright/test";
import { truncateAll } from "./helpers/db";
import { pruneSynapseContainers } from "./helpers/docker";

// Custom domains panel — exercises components/CustomDomainsPanel.tsx
// against the live compose stack. Per CLAUDE.md, the test suite runs
// against postgres + the API + a real provisioner, so we let the
// dashboard provision a real backend, then drive the per-deployment
// CustomDomainsPanel through add → list → verify → remove.
//
// Compose's default config does not export SYNAPSE_PUBLIC_IP, so domains
// land at status='pending' with the lastDnsError hint. That's exactly
// what the panel's pending-state branch + warning banner cover, so we
// assert against that shape rather than spinning up a fake DNS.

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

// Provision a deployment via the dashboard and return its allocated
// "<adj>-<animal>-<NNNN>" name. Mirrors the pattern in
// deployments.spec.ts so test setup stays consistent.
async function provisionDeployment(page: Page): Promise<string> {
  await page
    .getByRole("button", { name: /create deployment/i })
    .first()
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create", exact: true })
    .click();

  const nameLocator = page.getByText(/-[a-z]+-\d{4}/).first();
  await expect(nameLocator).toBeVisible({ timeout: 90_000 });
  const deploymentName = (await nameLocator.textContent())?.trim() ?? "";
  expect(deploymentName).toMatch(/^[a-z]+-[a-z]+-\d{4}$/);
  return deploymentName;
}

async function openDomainsPanel(page: Page, deploymentName: string) {
  await page
    .getByRole("button", {
      name: `Manage custom domains for ${deploymentName}`,
    })
    .click();
  await expect(
    page.getByTestId(`custom-domains-panel-${deploymentName}`),
  ).toBeVisible();
}

test.beforeEach(async () => {
  await truncateAll();
  pruneSynapseContainers();
});

test.afterEach(async () => {
  pruneSynapseContainers();
});

test("custom domains: empty state", async ({ page }) => {
  await setupProject(page);
  const deploymentName = await provisionDeployment(page);
  await openDomainsPanel(page, deploymentName);

  await expect(page.getByTestId("custom-domains-empty")).toBeVisible();
  await expect(page.getByTestId("custom-domain-dns-hint")).toBeVisible();
});

test("custom domains: add then remove", async ({ page }) => {
  await setupProject(page);
  const deploymentName = await provisionDeployment(page);
  await openDomainsPanel(page, deploymentName);

  // Add a single api-role domain. Compose's stack runs without
  // SYNAPSE_PUBLIC_IP set so the row lands at status='pending'.
  await page.getByTestId("custom-domain-input").fill("api.example.com");
  await page.getByTestId("custom-domain-add").click();

  const row = page.getByTestId("custom-domain-row-api.example.com");
  await expect(row).toBeVisible();
  await expect(
    page.getByTestId("custom-domain-status-api.example.com"),
  ).toHaveText("pending");

  // The unconfigured-public-IP banner fires once we have at least one
  // pending row carrying the hint.
  await expect(
    page.getByTestId("custom-domains-public-ip-warning"),
  ).toBeVisible();

  // Remove. Native confirm() — auto-accept it.
  page.on("dialog", (d) => d.accept());
  await page
    .getByRole("button", { name: "Remove custom domain api.example.com" })
    .click();
  await expect(row).toBeHidden();
  await expect(page.getByTestId("custom-domains-empty")).toBeVisible();
});

test("custom domains: form validates bad input", async ({ page }) => {
  await setupProject(page);
  const deploymentName = await provisionDeployment(page);
  await openDomainsPanel(page, deploymentName);

  // Empty input keeps the submit button disabled — that's the empty
  // case. We assert the disabled state rather than expecting the form
  // to surface an inline error.
  await expect(page.getByTestId("custom-domain-add")).toBeDisabled();

  // Obviously-broken hostname — no dot at all. The client-side regex
  // catches it before round-tripping to the backend.
  await page.getByTestId("custom-domain-input").fill("no-dot-here");
  await page.getByTestId("custom-domain-add").click();
  await expect(page.getByTestId("custom-domain-form-error")).toContainText(
    /hostname/i,
  );
});

test("custom domains: lists multiple rows", async ({ page }) => {
  await setupProject(page);
  const deploymentName = await provisionDeployment(page);
  await openDomainsPanel(page, deploymentName);

  // Add api-role first, then dashboard-role — verify both render.
  await page.getByTestId("custom-domain-input").fill("api.fechasul.com.br");
  await page.getByTestId("custom-domain-role").selectOption("api");
  await page.getByTestId("custom-domain-add").click();
  await expect(
    page.getByTestId("custom-domain-row-api.fechasul.com.br"),
  ).toBeVisible();

  await page.getByTestId("custom-domain-input").fill("admin.fechasul.com.br");
  await page.getByTestId("custom-domain-role").selectOption("dashboard");
  await page.getByTestId("custom-domain-add").click();
  await expect(
    page.getByTestId("custom-domain-row-admin.fechasul.com.br"),
  ).toBeVisible();

  // Both rows live under the same list container.
  const list = page.getByTestId("custom-domains-list");
  await expect(list.getByRole("listitem")).toHaveCount(2);
});
