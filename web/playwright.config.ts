import { defineConfig, devices } from "@playwright/test";

// End-to-end tests: the real engine (built from engine/) behind the Vite dev
// server, in Chromium. e2e/global-setup.ts starts both and stops them after.
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
    // The only dev origin the engine accepts (DIALGA_DEV=1).
    baseURL: "http://localhost:1420",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
