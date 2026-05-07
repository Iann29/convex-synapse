import {
  test,
  expect,
  type APIRequestContext,
  type Page,
  type Route,
} from "@playwright/test";
import { truncateAll } from "./helpers/db";

const API_BASE = process.env.SYNAPSE_API_URL || "http://localhost:8080";

// Host-domain admin panel — exercises components/HostDomainPanel.tsx,
// mounted at /admin/host-domain (instance-admin-only).
//
// Layout under /admin/* enforces the gate: non-admins are redirected to
// /teams. The Admin link in the top nav is conditional on
// is_instance_admin, so non-admins never see it either.
//
// The full apply path runs setup.sh on the host, which we can't invoke
// from a Playwright run. Instead, we mock /v1/admin/host_domain at the
// page-route level so we can drive the form + confirm modal without the
// backend needing to be wired up. (The PR that ships the backend half
// has its own Go integration coverage; this spec covers the UI wiring.)
//
// The actual end-to-end (real DNS swap → real Caddy reload → real
// redirect) is gated behind real-VPS smoke per CLAUDE.md.

// Register the FIRST user via the public API. The first registrant is
// auto-promoted to instance admin (synapse/internal/api/auth.go::register
// + migration 000013). Subsequent users are not admins.
async function registerAdmin(page: Page) {
  await page.goto("/register");
  await page.locator("#register-email").fill("admin@example.com");
  await page.locator("#register-password").fill("strongpass123");
  await page.locator("#register-name").fill("Instance Admin");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL(/\/teams\b/);
}

// Register a NON-admin (anyone after the first user). We use the API
// directly because the dashboard page would auto-login the new user
// over the existing admin session, which we don't want here.
async function seedSecondUser(
  request: APIRequestContext,
  email: string,
  name: string,
): Promise<void> {
  // Make sure there's already a first user (the admin).
  const adminLogin = await request.post(`${API_BASE}/v1/auth/login`, {
    data: { email: "admin@example.com", password: "strongpass123" },
  });
  if (!adminLogin.ok()) {
    throw new Error(`admin login failed: ${adminLogin.status()}`);
  }
  // Now register the second user — first-user promotion only fires when
  // the users table is empty, so this user is not an admin.
  const reg = await request.post(`${API_BASE}/v1/auth/register`, {
    data: { email, password: "strongpass123", name },
  });
  if (!reg.ok()) {
    throw new Error(`register ${email} failed: ${reg.status()}`);
  }
}

// Page-level mock for /v1/admin/host_domain GET. Drops the response we
// want into the dashboard's network without touching the real backend.
async function mockHostDomainGet(
  page: Page,
  body: Record<string, unknown>,
): Promise<void> {
  await page.route("**/v1/admin/host_domain", async (route: Route) => {
    if (route.request().method() === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(body),
      });
      return;
    }
    // Fall through for POST / others — the spec mocks those separately.
    await route.continue();
  });
}

test.beforeEach(async () => {
  await truncateAll();
});

test("non-admin user sees no Admin link and is redirected from /admin", async ({
  page,
  request,
}) => {
  // Seed: admin first (consumes the first-user promotion), then a
  // regular member.
  await page.goto("/register");
  await page.locator("#register-email").fill("admin@example.com");
  await page.locator("#register-password").fill("strongpass123");
  await page.locator("#register-name").fill("Admin");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL(/\/teams\b/);

  // Log out.
  await page.evaluate(() => window.localStorage.clear());

  // Register a second user via API + log in via UI.
  await seedSecondUser(request, "member@example.com", "Member");
  await page.goto("/login");
  await page.locator("#login-email").fill("member@example.com");
  await page.locator("#login-password").fill("strongpass123");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/teams\b/);

  // Top nav: the Admin link is absent for non-admins.
  await expect(page.getByTestId("topnav-admin-link")).toHaveCount(0);

  // Direct URL access to /admin/host-domain bounces to /teams.
  await page.goto("/admin/host-domain");
  await expect(page).toHaveURL(/\/teams\b/);
  // The host-domain panel never rendered.
  await expect(page.getByTestId("host-domain-panel")).toHaveCount(0);
});

test("admin sees the Admin link and the panel renders the current configuration", async ({
  page,
}) => {
  await registerAdmin(page);

  await mockHostDomainGet(page, {
    mode: "tls",
    domain: "synapse.example.com",
    publicUrl: "https://synapse.example.com",
    publicIp: "1.2.3.4",
    acmeEmail: "ops@example.com",
    fallbackUrls: ["http://1.2.3.4"],
  });

  // Top nav exposes the Admin link to instance admins.
  await expect(page.getByTestId("topnav-admin-link")).toBeVisible();
  await page.getByTestId("topnav-admin-link").click();
  await expect(page).toHaveURL(/\/admin\/host-domain\b/);

  // Sidebar entry under /admin
  await expect(page.getByTestId("admin-nav-host-domain")).toBeVisible();

  // Panel mounts and reads from the mocked endpoint.
  await expect(page.getByTestId("host-domain-panel")).toBeVisible();
  await expect(page.getByTestId("host-domain-mode-badge")).toHaveText(/HTTPS/i);
  await expect(page.getByTestId("host-domain-domain")).toContainText(
    "synapse.example.com",
  );
  await expect(page.getByTestId("host-domain-public-url")).toContainText(
    "https://synapse.example.com",
  );
  await expect(page.getByTestId("host-domain-public-ip")).toContainText(
    "1.2.3.4",
  );
  await expect(page.getByTestId("host-domain-acme-email")).toContainText(
    "ops@example.com",
  );
});

test("admin enters an invalid domain and the form rejects it", async ({
  page,
}) => {
  await registerAdmin(page);

  await mockHostDomainGet(page, {
    mode: "plain",
    publicUrl: "http://1.2.3.4",
    publicIp: "1.2.3.4",
    fallbackUrls: ["http://1.2.3.4"],
  });

  await page.goto("/admin/host-domain");
  await expect(page.getByTestId("host-domain-panel")).toBeVisible();

  // Open the change form.
  await page.getByTestId("host-domain-change-open").click();
  await expect(page.getByTestId("host-domain-change-form")).toBeVisible();

  // Default mode is whichever the current config is — "plain" → tls
  // doesn't get auto-selected, so flip to TLS explicitly.
  await page.getByTestId("host-domain-mode-tls").click();

  // Type a clearly broken hostname (no dot).
  await page.getByTestId("host-domain-domain-input").fill("not-a-domain");

  // Apply button is gated by the validator — it gets disabled when the
  // input fails the regex, so an attempted .click() would time out
  // (Playwright won't click disabled buttons). Just verify the
  // disabled state.
  await expect(page.getByTestId("host-domain-apply")).toBeDisabled();
});

test("admin enters a valid domain and the confirm modal appears", async ({
  page,
}) => {
  await registerAdmin(page);

  await mockHostDomainGet(page, {
    mode: "plain",
    publicUrl: "http://1.2.3.4",
    publicIp: "1.2.3.4",
    fallbackUrls: ["http://1.2.3.4"],
  });

  await page.goto("/admin/host-domain");
  await page.getByTestId("host-domain-change-open").click();
  await page.getByTestId("host-domain-mode-tls").click();
  await page
    .getByTestId("host-domain-domain-input")
    .fill("synapse.example.com");

  // The DNS hint surfaces the operator's IP so they know which A
  // record to add.
  await expect(page.getByTestId("host-domain-dns-hint")).toContainText(
    "1.2.3.4",
  );

  await page.getByTestId("host-domain-apply").click();

  // Confirm modal lands. Shows from-URL → to-URL diff.
  await expect(page.getByTestId("host-domain-confirm-dialog")).toBeVisible();
  await expect(page.getByTestId("host-domain-confirm-target")).toHaveText(
    "https://synapse.example.com",
  );
  // Always show the fallback URL prominently — it's the operator's
  // safety net if DNS fails.
  await expect(page.getByTestId("host-domain-confirm-fallback")).toContainText(
    "http://1.2.3.4",
  );
});

test("cancelling the confirm modal returns to the form unchanged", async ({
  page,
}) => {
  await registerAdmin(page);

  await mockHostDomainGet(page, {
    mode: "plain",
    publicUrl: "http://1.2.3.4",
    publicIp: "1.2.3.4",
    fallbackUrls: ["http://1.2.3.4"],
  });

  await page.goto("/admin/host-domain");
  await page.getByTestId("host-domain-change-open").click();
  await page.getByTestId("host-domain-mode-tls").click();
  await page
    .getByTestId("host-domain-domain-input")
    .fill("synapse.example.com");
  await page.getByTestId("host-domain-apply").click();

  await expect(page.getByTestId("host-domain-confirm-dialog")).toBeVisible();
  await page.getByTestId("host-domain-confirm-cancel").click();
  await expect(page.getByTestId("host-domain-confirm-dialog")).toBeHidden();

  // The form is still on screen with the user's input intact.
  await expect(page.getByTestId("host-domain-change-form")).toBeVisible();
  await expect(page.getByTestId("host-domain-domain-input")).toHaveValue(
    "synapse.example.com",
  );
});

// Apply flow with a real daemon is covered by real-VPS smoke per
// CLAUDE.md. We can't exercise it here without standing up the host-side
// daemon socket the backend talks to. The mock infra is in place
// (mockHostDomainGet) — extending it to mock POST + status polling is
// possible but brittle in Playwright's event-loop scheduling, so we
// leave the live path to the real-VPS gate.
test.skip("apply flow polls status and redirects on success", () => {
  // intentionally skipped — see comment block above.
});
