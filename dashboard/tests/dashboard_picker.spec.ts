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

// Seed a deployment row directly. The embed page only needs the row in
// the DB to fetch /v1/deployments/<name>/auth — no docker container is
// required for the picker overlay to render (the iframe will fail to
// load, but the parent page's overlay is independent of that).
async function seedDeployment(
  projectId: string,
  name: string,
  type: "dev" | "prod" | "preview",
  isDefault: boolean,
  hostPort: number,
): Promise<void> {
  const c = new Client({ connectionString: DB_URL });
  await c.connect();
  try {
    await c.query(
      `INSERT INTO deployments (project_id, name, deployment_type, status,
                                 admin_key, instance_secret, host_port,
                                 is_default, deployment_url, container_id)
       VALUES ($1, $2, $3, 'running', $4, 'fake-secret', $5, $6, $7, $8)`,
      [
        projectId,
        name,
        type,
        `fake-admin-${name}`,
        hostPort,
        isDefault,
        `http://127.0.0.1:${hostPort}`,
        `fake-container-${name}`,
      ],
    );
    // Mirror into deployment_replicas so loadDeployment() works.
    await c.query(
      `INSERT INTO deployment_replicas (deployment_id, replica_index, container_id, host_port, status)
         SELECT id, 0, $2, $3, 'running' FROM deployments WHERE name = $1`,
      [name, `fake-container-${name}`, hostPort],
    );
  } finally {
    await c.end();
  }
}

async function lookupProjectId(slug: string): Promise<string> {
  const c = new Client({ connectionString: DB_URL });
  await c.connect();
  try {
    const deadline = Date.now() + 3000;
    while (Date.now() < deadline) {
      const r = await c.query<{ id: string }>(
        `SELECT id FROM projects WHERE slug = $1 LIMIT 1`,
        [slug],
      );
      if (r.rows[0]) {
        return r.rows[0].id;
      }
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    throw new Error(`project ${slug} was not created within 3s`);
  } finally {
    await c.end();
  }
}

test.beforeEach(async () => {
  await truncateAll();
});

test("picker pill renders with deployment type colour", async ({ page }) => {
  await registerViaUI(page);
  await page.getByRole("button", { name: "Create team" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("Pick Co");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: /pick co/i }).click();
  await page.getByRole("button", { name: "Create project" }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#project-name").fill("Pick Project");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  const projectId = await lookupProjectId("pick-project");
  await seedDeployment(projectId, "lonely-fox-1", "dev", true, 5101);

  await page.goto("/embed/lonely-fox-1");
  // Picker pill renders.
  const pill = page.getByTestId("deployment-picker-pill");
  await expect(pill).toBeVisible();
  await expect(pill).toContainText("Development");
  await expect(pill).toContainText("lonely-fox-1");
  // Single-deployment mode → button is disabled (no dropdown).
  await expect(pill).toBeDisabled();
});

test("picker dropdown groups deployments by type and switches via click", async ({
  page,
}) => {
  await registerViaUI(page);
  await page.getByRole("button", { name: "Create team" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("Multi Co");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: /multi co/i }).click();
  await page.getByRole("button", { name: "Create project" }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#project-name").fill("Multi Project");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  const projectId = await lookupProjectId("multi-project");
  await seedDeployment(projectId, "alpha-dev-1", "dev", true, 5201);
  await seedDeployment(projectId, "alpha-prod-1", "prod", true, 5202);
  await seedDeployment(projectId, "alpha-preview-1", "preview", false, 5203);

  await page.goto("/embed/alpha-dev-1");
  const pill = page.getByTestId("deployment-picker-pill");
  await expect(pill).toBeVisible();
  await expect(pill).toContainText("alpha-dev-1");

  // Open the dropdown.
  await pill.click();
  const menu = page.getByTestId("deployment-picker-menu");
  await expect(menu).toBeVisible();
  await expect(menu).toContainText("Production");
  await expect(menu).toContainText("Development");
  await expect(menu).toContainText("Preview Deployments");

  // Click the prod entry → URL flips, fresh embed loads.
  await page.getByTestId("deployment-picker-item-alpha-prod-1").click();
  await expect(page).toHaveURL(/\/embed\/alpha-prod-1\b/);
  // Pill reflects new context.
  await expect(page.getByTestId("deployment-picker-pill")).toContainText(
    "alpha-prod-1",
  );
});

test("breadcrumb in picker header links back to project page", async ({
  page,
}) => {
  await registerViaUI(page);
  await page.getByRole("button", { name: "Create team" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("Crumb Co");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: /crumb co/i }).click();
  await page.getByRole("button", { name: "Create project" }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#project-name").fill("Crumb Project");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  const projectId = await lookupProjectId("crumb-project");
  await seedDeployment(projectId, "trail-1", "dev", true, 5301);

  await page.goto("/embed/trail-1");
  await expect(page.getByTestId("deployment-picker-pill")).toBeVisible();

  // Header breadcrumb has links to team + project.
  const teamLink = page.getByRole("link", { name: "Crumb Co" });
  const projectLink = page.getByRole("link", { name: "Crumb Project" });
  await expect(teamLink).toBeVisible();
  await expect(projectLink).toBeVisible();
  // Clicking the project name lands us on the project page.
  await projectLink.click();
  await expect(page).toHaveURL(new RegExp(`/teams/crumb-co/${projectId}`));
});

test("Ctrl+Alt+1 switches to a prod deployment when one exists", async ({
  page,
}) => {
  await registerViaUI(page);
  await page.getByRole("button", { name: "Create team" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("Hotkey Co");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: /hotkey co/i }).click();
  await page.getByRole("button", { name: "Create project" }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#project-name").fill("Hotkey Project");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  const projectId = await lookupProjectId("hotkey-project");
  await seedDeployment(projectId, "dev-tab", "dev", true, 5401);
  await seedDeployment(projectId, "prod-tab", "prod", true, 5402);

  await page.goto("/embed/dev-tab");
  await expect(page.getByTestId("deployment-picker-pill")).toContainText(
    "dev-tab",
  );

  // Ctrl+Alt+1 → first prod (prod-tab).
  await page.keyboard.press("Control+Alt+Digit1");
  await expect(page).toHaveURL(/\/embed\/prod-tab\b/);
});

// ---------- v1.1 polish: status dot + keyboard nav + search ----------

test("pill carries a status indicator alongside the type dot", async ({
  page,
}) => {
  await registerViaUI(page);
  await page.getByRole("button", { name: "Create team" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("Status Co");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: /status co/i }).click();
  await page.getByRole("button", { name: "Create project" }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#project-name").fill("Status Project");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  const projectId = await lookupProjectId("status-project");
  await seedDeployment(projectId, "running-one", "dev", true, 5501);

  await page.goto("/embed/running-one");
  // Status dot is its own element so we can pin it via testid.
  await expect(page.getByTestId("deployment-picker-status")).toBeVisible();
  await expect(page.getByTestId("deployment-picker-status")).toHaveAttribute(
    "aria-label",
    /running/i,
  );
});

test("keyboard nav: arrow down + Enter switches deployments", async ({
  page,
}) => {
  await registerViaUI(page);
  await page.getByRole("button", { name: "Create team" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("KbCo");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: /kbco/i }).click();
  await page.getByRole("button", { name: "Create project" }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#project-name").fill("Kb Project");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  const projectId = await lookupProjectId("kb-project");
  await seedDeployment(projectId, "kb-prod", "prod", true, 5601);
  await seedDeployment(projectId, "kb-dev", "dev", true, 5602);

  await page.goto("/embed/kb-dev");
  // Open the dropdown.
  await page.getByTestId("deployment-picker-pill").click();
  await expect(page.getByTestId("deployment-picker-menu")).toBeVisible();

  // Down once highlights index 0 (first prod). Enter selects it.
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/embed\/kb-prod\b/);
});

test("Escape closes the dropdown", async ({ page }) => {
  await registerViaUI(page);
  await page.getByRole("button", { name: "Create team" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("EscCo");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: /escco/i }).click();
  await page.getByRole("button", { name: "Create project" }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#project-name").fill("Esc Project");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  const projectId = await lookupProjectId("esc-project");
  await seedDeployment(projectId, "esc-a", "dev", true, 5701);
  await seedDeployment(projectId, "esc-b", "prod", true, 5702);

  await page.goto("/embed/esc-a");
  await page.getByTestId("deployment-picker-pill").click();
  await expect(page.getByTestId("deployment-picker-menu")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("deployment-picker-menu")).toBeHidden();
});

test("search filter shows up at 6+ deployments and narrows the list", async ({
  page,
}) => {
  await registerViaUI(page);
  await page.getByRole("button", { name: "Create team" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("SearchCo");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: /searchco/i }).click();
  await page.getByRole("button", { name: "Create project" }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#project-name").fill("Many Project");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  const projectId = await lookupProjectId("many-project");
  // Seed 6 deployments so the search threshold (6+) trips.
  await seedDeployment(projectId, "alpha-prod", "prod", true, 5801);
  await seedDeployment(projectId, "alpha-dev", "dev", true, 5802);
  await seedDeployment(projectId, "alpha-preview", "preview", false, 5803);
  await seedDeployment(projectId, "beta-prod", "prod", false, 5804);
  await seedDeployment(projectId, "beta-dev", "dev", false, 5805);
  await seedDeployment(projectId, "beta-preview", "preview", false, 5806);

  await page.goto("/embed/alpha-dev");
  await page.getByTestId("deployment-picker-pill").click();

  // Search input is rendered.
  const search = page.getByTestId("deployment-picker-search");
  await expect(search).toBeVisible();

  // All 6 items present pre-filter.
  await expect(page.getByTestId("deployment-picker-item-alpha-prod")).toBeVisible();
  await expect(page.getByTestId("deployment-picker-item-beta-preview")).toBeVisible();

  // Filter by "alpha" — keeps only the 3 alpha rows.
  await search.fill("alpha");
  await expect(page.getByTestId("deployment-picker-item-alpha-prod")).toBeVisible();
  await expect(page.getByTestId("deployment-picker-item-alpha-dev")).toBeVisible();
  await expect(page.getByTestId("deployment-picker-item-alpha-preview")).toBeVisible();
  await expect(page.getByTestId("deployment-picker-item-beta-prod")).toBeHidden();
  await expect(page.getByTestId("deployment-picker-item-beta-dev")).toBeHidden();
  await expect(page.getByTestId("deployment-picker-item-beta-preview")).toBeHidden();

  // Filter by deployment type — also matches.
  await search.fill("preview");
  await expect(page.getByTestId("deployment-picker-item-alpha-preview")).toBeVisible();
  await expect(page.getByTestId("deployment-picker-item-beta-preview")).toBeVisible();
  await expect(page.getByTestId("deployment-picker-item-alpha-prod")).toBeHidden();

  // Empty state when nothing matches.
  await search.fill("zzznope");
  await expect(page.getByText(/No deployments match/i)).toBeVisible();
});

test("'/' hotkey opens the dropdown and focuses the search input", async ({
  page,
}) => {
  await registerViaUI(page);
  await page.getByRole("button", { name: "Create team" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#team-name").fill("SlashCo");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("link", { name: /slashco/i }).click();
  await page.getByRole("button", { name: "Create project" }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#project-name").fill("Slash Project");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  const projectId = await lookupProjectId("slash-project");
  // 6+ to render the search input.
  for (let i = 0; i < 6; i++) {
    await seedDeployment(projectId, `slash-${i}`, "dev", i === 0, 5900 + i);
  }

  await page.goto("/embed/slash-0");
  // Ensure the picker has mounted before driving keyboard — the
  // listener that handles "/" registers via useEffect and isn't
  // there during the initial loading splash.
  await expect(page.getByTestId("deployment-picker-pill")).toBeVisible();

  // Press "/" — dropdown opens, search gains focus.
  await page.keyboard.press("/");
  await expect(page.getByTestId("deployment-picker-menu")).toBeVisible();
  const search = page.getByTestId("deployment-picker-search");
  await expect(search).toBeFocused();
});
