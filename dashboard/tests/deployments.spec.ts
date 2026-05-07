import { test, expect, type Page } from "@playwright/test";
import { Client } from "pg";
import { truncateAll } from "./helpers/db";
import {
  listSynapseContainerNames,
  pruneSynapseContainers,
} from "./helpers/docker";

const DB_URL =
  process.env.SYNAPSE_DB_URL ||
  "postgres://synapse:synapse@localhost:5432/synapse";

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

  // Project URL uses the project UUID (the dashboard renders the slug
  // inside the card but routes by id for stability across renames).
  await expect(page).toHaveURL(/\/teams\/amage\/[0-9a-f-]{36}\b/);
}

async function seedDeploymentWithDeployKey(
  projectId: string,
  deploymentName: string,
) {
  const c = new Client({ connectionString: DB_URL });
  await c.connect();
  try {
    await c.query(
      `INSERT INTO deployments (project_id, name, deployment_type, status,
                                admin_key, instance_secret, host_port,
                                is_default, deployment_url, container_id)
       VALUES ($1, $2, 'dev', 'running', 'fake-admin', 'fake-secret',
               3499, true, 'http://127.0.0.1:3499', $3)`,
      [projectId, deploymentName, `fake-container-${deploymentName}`],
    );
    await c.query(
      `INSERT INTO deployment_replicas (deployment_id, replica_index, container_id, host_port, status)
         SELECT id, 0, $2, 3499, 'running' FROM deployments WHERE name = $1`,
      [deploymentName, `fake-container-${deploymentName}`],
    );
    await c.query(
      `INSERT INTO deploy_keys (deployment_id, name, admin_key_prefix, admin_key_hash, created_by)
         SELECT d.id, 'github-actions', 'abcd1234', 'hash-github-actions', u.id
           FROM deployments d
           JOIN users u ON u.email = 'ian@example.com'
          WHERE d.name = $1`,
      [deploymentName],
    );
  } finally {
    await c.end();
  }
}

test.beforeEach(async () => {
  await truncateAll();
  pruneSynapseContainers();
});

test.afterEach(async () => {
  pruneSynapseContainers();
});

test("provision a deployment via the dashboard", async ({ page }) => {
  await setupProject(page);

  await page.getByRole("button", { name: /create deployment/i }).first().click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  // Wait for the deployment row — friendly name "<adj>-<animal>-<NNNN>".
  await expect(page.getByText(/-[a-z]+-\d{4}/).first()).toBeVisible({
    timeout: 90_000,
  });

  await expect
    .poll(() => listSynapseContainerNames().length, {
      timeout: 30_000,
      intervals: [500, 1000, 2000],
    })
    .toBeGreaterThan(0);

  const containers = listSynapseContainerNames();
  expect(containers[0]).toMatch(/^convex-/);
});

test("delete a deployment via the dashboard", async ({ page }) => {
  await setupProject(page);

  // Provision first.
  await page.getByRole("button", { name: /create deployment/i }).first().click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create", exact: true })
    .click();

  // Capture the deployment name from the rendered row.
  const nameLocator = page.getByText(/-[a-z]+-\d{4}/).first();
  await expect(nameLocator).toBeVisible({ timeout: 90_000 });
  const deploymentName = (await nameLocator.textContent())?.trim() ?? "";
  expect(deploymentName).toMatch(/^[a-z]+-[a-z]+-\d{4}$/);

  // Auto-accept the native confirm() that the delete handler raises.
  page.on("dialog", (d) => d.accept());

  await page
    .getByRole("button", { name: new RegExp(`delete deployment ${deploymentName}`, "i") })
    .click();

  // Row disappears from the list.
  await expect(nameLocator).toBeHidden({ timeout: 15_000 });

  // Container is gone on the host too.
  await expect
    .poll(() => listSynapseContainerNames(), {
      timeout: 15_000,
    })
    .toEqual([]);
});

test("deploy key revoke prompt explains credential rotation", async ({ page }) => {
  await setupProject(page);

  const projectId = page.url().match(/\/teams\/amage\/([0-9a-f-]{36})/)?.[1];
  expect(projectId).toBeTruthy();
  const deploymentName = "seeded-owl-1111";
  await seedDeploymentWithDeployKey(projectId!, deploymentName);
  await page.reload();
  await expect(page.getByText(deploymentName)).toBeVisible();

  await page
    .getByRole("button", {
      name: new RegExp(`manage deploy keys for ${deploymentName}`, "i"),
    })
    .click();

  await expect(page.getByText("github-actions")).toBeVisible();

  page.once("dialog", (d) => {
    expect(d.message()).toContain("rotate this deployment's credentials");
    void d.dismiss();
  });
  await page
    .getByRole("button", { name: /revoke deploy key github-actions/i })
    .click();
  await expect(page.getByText("github-actions")).toBeVisible();
});
