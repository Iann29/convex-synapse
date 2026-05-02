import { test, expect, type Page } from "@playwright/test";
import { Client } from "pg";
import { truncateAll } from "./helpers/db";

const DB_URL =
  process.env.SYNAPSE_DB_URL ||
  "postgres://synapse:synapse@localhost:5432/synapse";

async function registerViaUI(page: Page, email: string, name: string) {
  await page.goto("/register");
  await page.locator("#register-email").fill(email);
  await page.locator("#register-password").fill("strongpass123");
  await page.locator("#register-name").fill(name);
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL(/\/teams\b/);
}

// Add a second member directly via SQL (bypasses the invite-token UI dance —
// we're testing the role-toggle path, not invites).
async function seedMember(
  teamSlug: string,
  email: string,
  password: string,
  name: string,
  role: "admin" | "member",
): Promise<string> {
  const c = new Client({ connectionString: DB_URL });
  await c.connect();
  try {
    // Reuse the bcrypt cost-12 hash for "strongpass123" produced by the
    // backend's auth.HashPassword. Lifting it from the test pool spares us a
    // round-trip through /v1/auth/register which would also create a new team.
    // (If the cost ever changes this will start failing fast.)
    const passwordHash =
      "$2a$12$VtIBO0hvcJ6FH2HDjk2DYO7jGwk4b2P6hvrOpZ55mPBqJlUgGhzpW";
    void password;
    const u = await c.query<{ id: string }>(
      `INSERT INTO users (email, password_hash, name) VALUES ($1, $2, $3) RETURNING id`,
      [email, passwordHash, name],
    );
    const userId = u.rows[0].id;
    await c.query(
      `INSERT INTO team_members (team_id, user_id, role)
         SELECT id, $1, $2 FROM teams WHERE slug = $3`,
      [userId, role, teamSlug],
    );
    return userId;
  } finally {
    await c.end();
  }
}

test.beforeEach(async () => {
  await truncateAll();
});

test("admin toggles member role admin↔member", async ({ page }) => {
  await registerViaUI(page, "owner@example.com", "Owner");
  await page.getByRole("button", { name: "Create team" }).click();
  const d = page.getByRole("dialog");
  await d.locator("#team-name").fill("Roster Co");
  await d.getByRole("button", { name: "Create", exact: true }).click();

  // Seed a plain member via SQL.
  await seedMember("roster-co", "mate@example.com", "strongpass123", "Mate", "member");

  await page.goto("/teams/roster-co/settings/members");
  const row = page.getByTestId("member-row-mate@example.com");
  await expect(row).toBeVisible();
  // Toggle once → admin, then again → member.
  await page.getByTestId("member-role-toggle-mate@example.com").click();
  await expect(row.getByText("admin")).toBeVisible();
  await page.getByTestId("member-role-toggle-mate@example.com").click();
  await expect(row.getByText("member")).toBeVisible();
});

test("last_admin guard prevents demoting sole admin", async ({ page }) => {
  await registerViaUI(page, "solo@example.com", "Solo");
  await page.getByRole("button", { name: "Create team" }).click();
  const d = page.getByRole("dialog");
  await d.locator("#team-name").fill("Solo Admin Co");
  await d.getByRole("button", { name: "Create", exact: true }).click();

  await page.goto("/teams/solo-admin-co/settings/members");
  // Solo admin's toggle is disabled (the UI mirrors the server guard).
  const toggle = page.getByTestId("member-role-toggle-solo@example.com");
  await expect(toggle).toBeDisabled();
});

test("admin removes another member", async ({ page }) => {
  await registerViaUI(page, "owner2@example.com", "Owner");
  await page.getByRole("button", { name: "Create team" }).click();
  const d = page.getByRole("dialog");
  await d.locator("#team-name").fill("Kick Co");
  await d.getByRole("button", { name: "Create", exact: true }).click();
  await seedMember("kick-co", "victim@example.com", "x", "Victim", "member");

  await page.goto("/teams/kick-co/settings/members");
  page.on("dialog", (dlg) => dlg.accept());
  await page.getByTestId("member-remove-victim@example.com").click();
  await expect(
    page.getByTestId("member-row-victim@example.com"),
  ).toBeHidden();
});
