import { defineConfig, devices } from "@playwright/test";

// End-to-end tests in Chromium: the production build of the app against the
// real engine (built from engine/). e2e/global-setup.ts starts both and
// stops them after; e2e/fixtures.ts serves the app on the dev origin.
export default defineConfig({
  testDir: "e2e",
  globalSetup: "./e2e/global-setup.ts",
  // One engine and one configuration file that the tests rewrite: in order.
  workers: 1,
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI
    ? [["list"], ["junit", { outputFile: "test-results/e2e-junit.xml" }]]
    : "list",
  use: {
    // The dev origin the engine accepts (DIALGA_DEV=1); nothing has to listen there.
    baseURL: "http://localhost:1420",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
