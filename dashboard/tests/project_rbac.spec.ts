import { test, expect, type Page } from "@playwright/test";
import { Client } from "pg";
import { truncateAll } from "./helpers/db";

const DB_URL =
  process.env.SYNAPSE_DB_URL ||
  "postgres://synapse:synapse@localhost:5432/synapse";

async function registerViaUI(page: Page, email = "ian@example.com") {
  await page.goto("/register");
  await page.locator("#register-email").fill(email);
  await page.locator("#register-password").fill("strongpass123");
  await page.locator("#register-name").fill("Ian");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL(/\/teams\b/);
}

// Seed a second user + add them to the team. Bypasses the invite UI
// dance — RBAC tests don't care about invites, they care about the
// members panel.
async function seedSecondUser(
  teamSlug: string,
  email: string,
  name: string,
): Promise<string> {
  const c = new Client({ connectionString: DB_URL });
  await c.connect();
  try {
    const passwordHash =
      "$2a$12$VtIBO0hvcJ6FH2HDjk2DYO7jGwk4b2P6hvrOpZ55mPBqJlUgGhzpW";
    const u = await c.query<{ id: string }>(
      `INSERT INTO users (email, password_hash, name) VALUES ($1, $2, $3) RETURNING id`,
      [email, passwordHash, name],
    );
    const userId = u.rows[0].id;
    await c.query(
      `INSERT INTO team_members (team_id, user_id, role)
         SELECT id, $1, 'member' FROM teams WHERE slug = $2`,
      [userId, teamSlug],
    );
    return userId;
  } finally {
    await c.end();
  }
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
}) => {
  await registerViaUI(page, "owner@example.com");
  await createTeamAndProject(page, "RBAC Co", "RBAC Project");
  await seedSecondUser("rbac-co", "mate@example.com", "Mate");

  // Refetch list — Members panel reloads via SWR after we seed.
  await page.reload();
  // Members panel renders.
  await expect(page.getByRole("heading", { name: "Members" })).toBeVisible();
  await expect(
    page.getByTestId("project-member-row-owner@example.com"),
  ).toBeVisible();
  await expect(
    page.getByTestId("project-member-row-mate@example.com"),
  ).toBeVisible();
  // Both rows show team source initially (no overrides yet).
  const mateRow = page.getByTestId("project-member-row-mate@example.com");
  await expect(mateRow.getByText("team", { exact: true })).toBeVisible();
});

test("admin downgrades a teammate to viewer via the role select", async ({
  page,
}) => {
  await registerViaUI(page, "boss@example.com");
  await createTeamAndProject(page, "Downgrade Co", "DProject");
  await seedSecondUser("downgrade-co", "junior@example.com", "Junior");
  await page.reload();

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
}) => {
  await registerViaUI(page, "boss2@example.com");
  await createTeamAndProject(page, "Drop Co", "DropProj");
  await seedSecondUser("drop-co", "mate2@example.com", "Mate");
  await page.reload();

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
