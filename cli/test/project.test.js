const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const {
  deploymentNameForTarget,
  projectConfigPath,
  readProjectConfig,
  writeProjectConfig,
} = require("../lib/project");

test("writes project metadata without Synapse tokens or deployment secrets", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "synapse-project-"));
  try {
    const file = writeProjectConfig(dir, {
      synapseUrl: "https://synapse.example.com",
      accessToken: "access-secret",
      refreshToken: "refresh-secret",
      team: {
        id: "team-id",
        slug: "team",
        name: "Team",
        accessToken: "team-secret",
      },
      project: {
        id: "project-id",
        slug: "app",
        name: "App",
        adminKey: "project-secret",
      },
      deployments: {
        dev: {
          id: "dev-id",
          name: "dev-cat",
          deploymentType: "dev",
          adminKey: "dev-secret",
          instanceSecret: "instance-secret",
        },
        prod: {
          id: "prod-id",
          name: "prod-cat",
          deploymentType: "prod",
          adminKey: "prod-secret",
        },
      },
    });

    assert.equal(file, projectConfigPath(dir));
    const raw = fs.readFileSync(file, "utf8");
    assert.equal(raw.includes("secret"), false);

    const config = readProjectConfig(dir);
    assert.equal(config.synapseUrl, "https://synapse.example.com");
    assert.equal(config.team.slug, "team");
    assert.equal(config.project.id, "project-id");
    assert.equal(config.deployments.dev.name, "dev-cat");
    assert.equal(config.deployments.prod.name, "prod-cat");
    assert.equal(deploymentNameForTarget(config, "dev"), "dev-cat");
    assert.equal(deploymentNameForTarget(config, "prod"), "prod-cat");
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
