import {
  test,
  expect,
  type APIRequestContext,
  type Page,
  type Route,
} from "@playwright/test";
import { truncateAll } from "./helpers/db";
import { pruneSynapseContainers } from "./helpers/docker";

// DNS auto-configuration UI — exercises:
//   - components/DnsCredentialsPanel.tsx mounted at /admin/dns-credentials
//   - the auto-config path bolted onto components/CustomDomainsPanel.tsx
//
// Backend (PR A) ships /v1/admin/dns_credentials/* and the
// /v1/internal/dns_provider detection probe. We don't depend on the
// backend half — every endpoint these specs care about is mocked via
// page.route so the UI half can land first.
//
// The host-domain spec already covers the cross-cutting "Admin nav
// hides for non-admins" gate; we don't repeat it here. We exercise the
// panel-level empty state, the add+delete-credential flow, and the
// per-deployment custom-domain UI's reaction to the detection probe.
//
// Pattern: per CLAUDE.md, locators use stable data-testid + role-scoped
// queries to keep the spec robust against neighbour-row ordering.

const API_BASE = process.env.SYNAPSE_API_URL || "http://localhost:8080";

async function registerAdmin(page: Page) {
  await page.goto("/register");
  await page.locator("#register-email").fill("admin@example.com");
  await page.locator("#register-password").fill("strongpass123");
  await page.locator("#register-name").fill("Instance Admin");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL(/\/teams\b/);
}

async function seedSecondUser(
  request: APIRequestContext,
  email: string,
  name: string,
): Promise<void> {
  const adminLogin = await request.post(`${API_BASE}/v1/auth/login`, {
    data: { email: "admin@example.com", password: "strongpass123" },
  });
  if (!adminLogin.ok()) {
    throw new Error(`admin login failed: ${adminLogin.status()}`);
  }
  const reg = await request.post(`${API_BASE}/v1/auth/register`, {
    data: { email, password: "strongpass123", name },
  });
  if (!reg.ok()) {
    throw new Error(`register ${email} failed: ${reg.status()}`);
  }
}

// Mock GET /v1/admin/dns_credentials with the supplied list. POST /
// DELETE fall through unless the caller mocks them too.
async function mockListCredentials(
  page: Page,
  rows: unknown[],
): Promise<void> {
  await page.route("**/v1/admin/dns_credentials", async (route: Route) => {
    if (route.request().method() === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(rows),
      });
      return;
    }
    await route.continue();
  });
}

// One-shot mock for POST /v1/admin/dns_credentials/cloudflare. Replies
// with the row the test wants to land in the panel's list. The matched
// route is chained with the GET mock so the panel re-fetches the list
// after a successful POST and sees the new row.
async function mockAddCloudflare(
  page: Page,
  before: unknown[],
  after: unknown[],
  newRow: unknown,
): Promise<void> {
  let rows = before;
  await page.route("**/v1/admin/dns_credentials", async (route: Route) => {
    if (route.request().method() === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(rows),
      });
      return;
    }
    await route.continue();
  });
  await page.route(
    "**/v1/admin/dns_credentials/cloudflare",
    async (route: Route) => {
      if (route.request().method() === "POST") {
        rows = after;
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(newRow),
        });
        return;
      }
      await route.continue();
    },
  );
}

test.beforeEach(async () => {
  await truncateAll();
  pruneSynapseContainers();
});

test.afterEach(async () => {
  pruneSynapseContainers();
});

test("non-admin: DNS credentials sidebar entry is unreachable", async ({
  page,
  request,
}) => {
  // Seed the first user (admin), then a regular member.
  await page.goto("/register");
  await page.locator("#register-email").fill("admin@example.com");
  await page.locator("#register-password").fill("strongpass123");
  await page.locator("#register-name").fill("Admin");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL(/\/teams\b/);
  await page.evaluate(() => window.localStorage.clear());

  await seedSecondUser(request, "member@example.com", "Member");
  await page.goto("/login");
  await page.locator("#login-email").fill("member@example.com");
  await page.locator("#login-password").fill("strongpass123");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/teams\b/);

  // Direct /admin/dns-credentials access bounces to /teams — same
  // gate the host-domain spec verifies. The sidebar entry never
  // renders because /admin redirects before reaching the layout.
  await page.goto("/admin/dns-credentials");
  await expect(page).toHaveURL(/\/teams\b/);
  await expect(page.getByTestId("admin-nav-dns-credentials")).toHaveCount(0);
  await expect(page.getByTestId("dns-credentials-panel")).toHaveCount(0);
});

test("admin: empty list renders the empty-state copy", async ({ page }) => {
  await registerAdmin(page);
  await mockListCredentials(page, []);

  await page.goto("/admin/dns-credentials");
  await expect(page.getByTestId("dns-credentials-panel")).toBeVisible();
  await expect(page.getByTestId("admin-nav-dns-credentials")).toBeVisible();
  await expect(page.getByTestId("dns-credentials-empty")).toBeVisible();
  await expect(page.getByTestId("dns-credentials-empty")).toContainText(
    /No credentials yet/i,
  );
});

test("admin: opens the Add Cloudflare modal and shows the deep-link", async ({
  page,
}) => {
  await registerAdmin(page);
  await mockListCredentials(page, []);

  await page.goto("/admin/dns-credentials");
  await page.getByTestId("dns-credentials-add-cloudflare").click();

  const dialog = page.getByTestId("dns-credentials-add-dialog");
  await expect(dialog).toBeVisible();
  // Helper text + the deep-link to Cloudflare's token page render.
  await expect(dialog).toContainText(/Zone:DNS:Edit/);
  await expect(dialog).toContainText(/Zone:Zone:Read/);
  const link = page.getByTestId("dns-credential-cloudflare-link");
  await expect(link).toBeVisible();
  await expect(link).toHaveAttribute(
    "href",
    /dash\.cloudflare\.com\/profile\/api-tokens/,
  );

  // Eye-icon parity: pressing toggle flips the password field type to
  // text so the operator can sanity-check the pasted token.
  const tokenInput = page.getByTestId("dns-credential-token-input");
  await expect(tokenInput).toHaveAttribute("type", "password");
  await page.getByTestId("dns-credential-token-toggle").click();
  await expect(tokenInput).toHaveAttribute("type", "text");
});

test("admin: adds a Cloudflare credential and the row appears", async ({
  page,
}) => {
  await registerAdmin(page);
  const newCred = {
    id: "cred-abc",
    provider: "cloudflare",
    label: "Personal CF",
    zones: [
      { id: "z1", name: "example.com" },
      { id: "z2", name: "example.org" },
    ],
    createdAt: new Date().toISOString(),
  };
  await mockAddCloudflare(page, [], [newCred], newCred);

  await page.goto("/admin/dns-credentials");
  await page.getByTestId("dns-credentials-add-cloudflare").click();
  await page
    .getByTestId("dns-credential-label-input")
    .fill("Personal CF");
  await page
    .getByTestId("dns-credential-token-input")
    .fill("cf-token-123");
  await page.getByTestId("dns-credentials-add-submit").click();

  // Row lands. Zones expand on demand.
  const row = page.getByTestId("dns-credential-row-cred-abc");
  await expect(row).toBeVisible();
  await expect(
    page.getByTestId("dns-credential-label-cred-abc"),
  ).toHaveText("Personal CF");
  await page.getByTestId("dns-credential-toggle-zones-cred-abc").click();
  const zones = page.getByTestId("dns-credential-zones-cred-abc");
  await expect(zones).toContainText("example.com");
  await expect(zones).toContainText("example.org");
});

test("admin: delete a credential surfaces 409 in-use error", async ({
  page,
}) => {
  await registerAdmin(page);
  const cred = {
    id: "cred-xyz",
    provider: "cloudflare",
    label: "Used CF",
    zones: [{ id: "z1", name: "example.com" }],
    createdAt: new Date().toISOString(),
  };
  await mockListCredentials(page, [cred]);
  // Mock DELETE → 409 with the in-use code the panel keys off.
  await page.route(
    "**/v1/admin/dns_credentials/cred-xyz",
    async (route: Route) => {
      if (route.request().method() === "DELETE") {
        await route.fulfill({
          status: 409,
          contentType: "application/json",
          body: JSON.stringify({
            code: "credential_in_use",
            message: "this credential is in use by 2 domains",
          }),
        });
        return;
      }
      await route.continue();
    },
  );

  await page.goto("/admin/dns-credentials");
  await page.getByTestId("dns-credential-delete-cred-xyz").click();
  const dialog = page.getByTestId("dns-credentials-delete-dialog");
  await expect(dialog).toBeVisible();
  await page.getByTestId("dns-credentials-delete-confirm").click();

  // The panel surfaces the in-use hint AND the raw backend message.
  const errBox = page.getByTestId("dns-credentials-delete-error");
  await expect(errBox).toBeVisible();
  await expect(errBox).toContainText(/in use/i);
  await expect(errBox).toContainText("2 domains");
});

// ---------- Custom-domains auto-config UI ----------
//
// Exercising the per-deployment panel needs a project + deployment to
// hang it off. We provision a real one through the UI (matches the
// pattern in custom-domains.spec.ts) and then mock just the DNS-detect
// probe to drive the conditional UI.

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

test("custom domains: cloudflare-detected domain shows the auto-config UI", async ({
  page,
}) => {
  // Mock the public detect endpoint to return Cloudflare. Has to be
  // installed BEFORE we navigate the dashboard so the first call hits
  // the route handler, not the live backend.
  await page.route(
    "**/v1/internal/dns_provider*",
    async (route: Route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          provider: "cloudflare",
          nameservers: ["amy.ns.cloudflare.com", "todd.ns.cloudflare.com"],
        }),
      });
    },
  );
  // Mock the credential list — instance admin + a credential whose
  // zone matches "example.com".
  await mockListCredentials(page, [
    {
      id: "cred-cf",
      provider: "cloudflare",
      label: "Personal CF",
      zones: [{ id: "z1", name: "example.com" }],
      createdAt: new Date().toISOString(),
    },
  ]);

  await setupProject(page);
  const deploymentName = await provisionDeployment(page);
  await openDomainsPanel(page, deploymentName);

  await page.getByTestId("custom-domain-input").fill("api.example.com");

  // Cloudflare box renders, with the auto-config toggle on by default
  // and the matching credential pre-selected.
  await expect(
    page.getByTestId("custom-domain-cloudflare-box"),
  ).toBeVisible();
  await expect(
    page.getByTestId("custom-domain-autoconfigure-toggle"),
  ).toBeChecked();
  await expect(
    page.getByTestId("custom-domain-credential-select"),
  ).toHaveValue("cred-cf");
  // Manual instructions are hidden on the CF happy-path.
  await expect(page.getByTestId("custom-domain-dns-hint")).toHaveCount(0);
});

test("custom domains: unknown-provider domain shows manual instructions", async ({
  page,
}) => {
  await page.route(
    "**/v1/internal/dns_provider*",
    async (route: Route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ provider: "unknown", nameservers: [] }),
      });
    },
  );
  await mockListCredentials(page, []);

  await setupProject(page);
  const deploymentName = await provisionDeployment(page);
  await openDomainsPanel(page, deploymentName);

  await page.getByTestId("custom-domain-input").fill("api.somerandom.dev");

  // Manual instructions visible, "unknown" hint visible, no CF box.
  await expect(page.getByTestId("custom-domain-dns-hint")).toBeVisible();
  await expect(
    page.getByTestId("custom-domain-detection-unknown"),
  ).toBeVisible();
  await expect(
    page.getByTestId("custom-domain-cloudflare-box"),
  ).toHaveCount(0);
});
