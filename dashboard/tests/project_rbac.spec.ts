import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { truncateAll } from "./helpers/db";

const API_BASE = process.env.SYNAPSE_API_URL || "http://localhost:8080";

async function registerViaUI(page: Page, email = "ian@example.com") {
  await page.goto("/register");
  await page.locator("#register-email").fill(email);
  await page.locator("#register-password").fill("strongpass123");
  await page.locator("#register-name").fill("Ian");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL(/\/teams\b/);
}

// Add a second team member through the real REST surface — register
// + invite + accept. Uses APIRequestContext so the work happens in
// the same network namespace + connection pool the dashboard hits,
// avoiding the SQL-injection visibility race we saw on slower CI
// runners (INSERT lands but the dashboard's SWR fetch via Synapse
// returned the pre-insert snapshot for one beat).
async function inviteSecondMember(
  request: APIRequestContext,
  ownerEmail: string,
  ownerPassword: string,
  teamSlug: string,
  newEmail: string,
  newName: string,
): Promise<string> {
  // Owner login (the dashboard already has them logged in via UI but
  // we need the bearer here for fetch).
  const ownerRes = await request.post(`${API_BASE}/v1/auth/login`, {
    data: { email: ownerEmail, password: ownerPassword },
  });
  if (!ownerRes.ok()) {
    throw new Error(`owner login failed: ${ownerRes.status()}`);
  }
  const ownerData = await ownerRes.json();
  const ownerToken = ownerData.accessToken as string;

  // Register the new user.
  const regRes = await request.post(`${API_BASE}/v1/auth/register`, {
    data: { email: newEmail, password: "strongpass123", name: newName },
  });
  if (!regRes.ok()) {
    throw new Error(`register ${newEmail} failed: ${regRes.status()}`);
  }
  const regData = await regRes.json();
  const newUserId = regData.user.id as string;
  const newUserToken = regData.accessToken as string;

  // Owner mints an invite for the new email.
  const inviteRes = await request.post(
    `${API_BASE}/v1/teams/${encodeURIComponent(teamSlug)}/invite_team_member`,
    {
      headers: { Authorization: `Bearer ${ownerToken}` },
      data: { email: newEmail, role: "member" },
    },
  );
  if (!inviteRes.ok()) {
    throw new Error(`invite ${newEmail} failed: ${inviteRes.status()}`);
  }
  const inviteData = await inviteRes.json();
  const inviteToken = inviteData.inviteToken as string;

  // New user accepts.
  const acceptRes = await request.post(`${API_BASE}/v1/team_invites/accept`, {
    headers: { Authorization: `Bearer ${newUserToken}` },
    data: { token: inviteToken },
  });
  if (!acceptRes.ok()) {
    throw new Error(`accept ${newEmail} failed: ${acceptRes.status()}`);
  }
  return newUserId;
}

async function createTeamAndProject(
  page: Page,
  teamName: string,
  projectName: string,
): Promise<void> {
  await page.getByRole("button", { name: "Create team" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill(teamName);
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: new RegExp(teamName, "i") }).click();

  await page.getByRole("button", { name: "Create project" }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#project-name").fill(projectName);
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: new RegExp(projectName, "i") }).click();
}

test.beforeEach(async () => {
  await truncateAll();
});

test("admin sees members panel with team-source badge for everyone initially", async ({
  page,
  request,
}) => {
  await registerViaUI(page, "owner@example.com");
  await createTeamAndProject(page, "RBAC Co", "RBAC Project");
  await inviteSecondMember(
    request,
    "owner@example.com",
    "strongpass123",
    "rbac-co",
    "mate@example.com",
    "Mate",
  );

  // SWR fetched /list_members on first mount with only the owner.
  // After a fresh team-member row lands, reload to drop SWR cache
  // and let the panel pick up the second row.
  await page.reload();
  await expect(
    page.getByTestId("project-member-row-mate@example.com"),
  ).toBeVisible();
  await expect(page.getByRole("heading", { name: "Members" })).toBeVisible();
  await expect(
    page.getByTestId("project-member-row-owner@example.com"),
  ).toBeVisible();
  const mateRow = page.getByTestId("project-member-row-mate@example.com");
  await expect(mateRow.getByText("team", { exact: true })).toBeVisible();
});

test("admin downgrades a teammate to viewer via the role select", async ({
  page,
  request,
}) => {
  await registerViaUI(page, "boss@example.com");
  await createTeamAndProject(page, "Downgrade Co", "DProject");
  await inviteSecondMember(
    request,
    "boss@example.com",
    "strongpass123",
    "downgrade-co",
    "junior@example.com",
    "Junior",
  );

  await page.reload();
  await expect(
    page.getByTestId("project-member-row-junior@example.com"),
  ).toBeVisible();
  // Switch role select to viewer for the seeded mate.
  const select = page.getByTestId("project-member-role-junior@example.com");
  await select.selectOption("viewer");

  // The select reflects the new value and the override controls appear.
  await expect(select).toHaveValue("viewer");
  // "Drop override" affordance only renders on override rows (source=project).
  await expect(
    page.getByTestId("project-member-remove-junior@example.com"),
  ).toBeVisible();
});

test("admin drops the override and the row falls back to team", async ({
  page,
  request,
}) => {
  await registerViaUI(page, "boss2@example.com");
  await createTeamAndProject(page, "Drop Co", "DropProj");
  await inviteSecondMember(
    request,
    "boss2@example.com",
    "strongpass123",
    "drop-co",
    "mate2@example.com",
    "Mate",
  );

  await page.reload();
  await expect(
    page.getByTestId("project-member-row-mate2@example.com"),
  ).toBeVisible();
  // Set viewer override first.
  await page
    .getByTestId("project-member-role-mate2@example.com")
    .selectOption("viewer");
  await expect(
    page.getByTestId("project-member-remove-mate2@example.com"),
  ).toBeVisible();

  // Native confirm wraps Drop override.
  page.on("dialog", (d) => d.accept());
  await page.getByTestId("project-member-remove-mate2@example.com").click();

  // After remove the override controls disappear (override gone) and
  // the role select snaps back to the team-fallback value (member).
  await expect(
    page.getByTestId("project-member-remove-mate2@example.com"),
  ).toBeHidden();
  await expect(
    page.getByTestId("project-member-role-mate2@example.com"),
  ).toHaveValue("member");
});

// Viewer-side UX is covered by the Go integration suite (14 RBAC
// cases including viewer-can't-write, can-self-remove, etc). The
// dashboard surface is gated purely on `myRole === "admin"` derived
// from the merged listing, so a unit-style assertion that the role
// select disappears for non-admins doesn't add coverage on top of
// the backend tests. Skipping the cross-context login dance here.
//
// If we ever want to assert the read-only render path inside the
// dashboard, the cleanest path is to:
//   1. POST /v1/auth/login from the test runner to mint a fresh JWT,
//   2. setLocalStorage("synapse.auth", { accessToken, refreshToken, user }),
//   3. visit the project page as that user.
// The backend coverage already locks down the security; the UI
// assertion would just confirm a render branch.
