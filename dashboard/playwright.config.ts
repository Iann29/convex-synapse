import { defineConfig, devices } from "@playwright/test";

// All e2e tests assume the full stack is up:
//   docker compose up -d
// (postgres + synapse on :8080 + dashboard on :6790).
//
// CI brings up the stack as part of the workflow before running these tests.
// Locally, run `make compose-up` first.
//
// Tests reset DB state between runs via tests/helpers/db.ts — they expect
// direct postgres access on :5432 (the compose default).

export default defineConfig({
  testDir: "./tests",
  fullyParallel: false, // tests share DB state — serialise them
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  reporter: process.env.CI ? "github" : "list",

  use: {
    baseURL: process.env.PLAYWRIGHT_BASE_URL || "http://localhost:6790",
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    actionTimeout: 10_000,
    navigationTimeout: 15_000,
  },

  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],

  // Provisioning a Convex backend takes ~1s after the image is cached, but
  // up to ~30s on a cold pull. Keep test timeouts generous.
  timeout: 90_000,
  expect: { timeout: 10_000 },
});
