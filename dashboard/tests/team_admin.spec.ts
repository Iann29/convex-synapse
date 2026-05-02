import { test, expect, type Page } from "@playwright/test";
import { Client } from "pg";
import { truncateAll } from "./helpers/db";

const DB_URL =
  process.env.SYNAPSE_DB_URL ||
  "postgres://synapse:synapse@localhost:5432/synapse";

async function registerViaUI(page: Page) {
  await page.goto("/register");
  await page.locator("#register-email").fill("ian@example.com");
  await page.locator("#register-password").fill("strongpass123");
  await page.locator("#register-name").fill("Ian");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL(/\/teams\b/);
}

// Seed a deployment row directly. The team_has_deployments check counts
// rows where status <> 'deleted', so a lightweight INSERT is enough — no
// need to provision a real container just to exercise the UI guard.
async function seedDeployment(teamSlug: string, name: string): Promise<void> {
  const c = new Client({ connectionString: DB_URL });
  await c.connect();
  try {
    const r = await c.query<{ id: string }>(
      `INSERT INTO projects (team_id, name, slug)
         SELECT id, $1, $2 FROM teams WHERE slug = $3
       RETURNING id`,
      ["Seed Project", `seed-${name}`, teamSlug],
    );
    const projectId = r.rows[0].id;
    await c.query(
      `INSERT INTO deployments (project_id, name, deployment_type, status,
                                 admin_key, instance_secret)
       VALUES ($1, $2, 'dev', 'running', 'fake-admin', 'fake-secret')`,
      [projectId, name],
    );
  } finally {
    await c.end();
  }
}

test.beforeEach(async () => {
  await truncateAll();
});

test("update_team renames slug and the URL follows", async ({ page }) => {
  await registerViaUI(page);
  await page.getByRole("button", { name: "Create team" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("Original Co");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: /original co/i }).click();
  await page.goto("/teams/original-co/settings/general");

  // Edit name + slug, save.
  await page.locator("#settings-team-name").fill("Renamed Co");
  await page.locator("#settings-team-slug").fill("renamed-co");
  await page.getByTestId("team-save").click();

  // URL flips to the new slug; identity card reflects new state.
  await expect(page).toHaveURL(/\/teams\/renamed-co\/settings\/general\b/);
  await expect(page.getByTestId("team-name")).toHaveText("Renamed Co");
  await expect(page.getByTestId("team-slug")).toHaveText("renamed-co");
});

test("delete_team refuses with live deployment, allows after deployment goes", async ({
  page,
}) => {
  await registerViaUI(page);
  await page.getByRole("button", { name: "Create team" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("Delme");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  // Seed a deployment row via SQL — no docker provisioning, just enough for
  // the team_has_deployments guard to fire.
  await seedDeployment("delme", "seed-deploy-1");

  await page.goto("/teams/delme/settings/general");
  await page.getByTestId("team-delete-open").click();
  let teamDialog = page.getByRole("dialog");
  await teamDialog.locator("#team-delete-confirm").fill("Delme");
  await teamDialog.getByTestId("team-delete-confirm").click();
  await expect(
    teamDialog.getByText(/still owns live deployments/i),
  ).toBeVisible();
  await teamDialog.getByRole("button", { name: "Cancel" }).click();

  // Mark the deployment deleted and retry — same UI flow.
  const c = new Client({ connectionString: DB_URL });
  await c.connect();
  try {
    await c.query(`UPDATE deployments SET status = 'deleted'`);
  } finally {
    await c.end();
  }

  await page.getByTestId("team-delete-open").click();
  teamDialog = page.getByRole("dialog");
  await teamDialog.locator("#team-delete-confirm").fill("Delme");
  await teamDialog.getByTestId("team-delete-confirm").click();
  await expect(page).toHaveURL(/\/teams$/);
});
