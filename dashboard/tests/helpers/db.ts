// Direct-to-postgres helpers used by the test setup hooks. We bypass the
// API for cleanup so we can reset the DB without needing admin credentials.

import { Client } from "pg";

const DB_URL =
  process.env.SYNAPSE_DB_URL ||
  "postgres://synapse:synapse@localhost:5432/synapse";

const TABLES = [
  "audit_events",
  "provisioning_jobs",
  "deploy_keys",
  "deployments",
  "project_env_vars",
  "projects",
  "team_invites",
  "team_members",
  "teams",
  "access_tokens",
  "users",
];

export async function truncateAll(): Promise<void> {
  const client = new Client({ connectionString: DB_URL });
  await client.connect();
  try {
    await client.query(
      `TRUNCATE ${TABLES.join(", ")} RESTART IDENTITY CASCADE`,
    );
  } finally {
    await client.end();
  }
}

export async function listDeploymentNames(): Promise<string[]> {
  const client = new Client({ connectionString: DB_URL });
  await client.connect();
  try {
    const r = await client.query<{ name: string }>(
      `SELECT name FROM deployments WHERE status <> 'deleted'`,
    );
    return r.rows.map((row) => row.name);
  } finally {
    await client.end();
  }
}
