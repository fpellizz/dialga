import { test as base } from "@playwright/test";

export { expect } from "@playwright/test";

/** The dev origin the engine accepts with DIALGA_DEV=1 (playwright.config.ts baseURL). */
const DEV_ORIGIN = "http://localhost:1420";

/**
 * Every page loads from DEV_ORIGIN, but Chromium fetches it from the build
 * served by e2e/global-setup.ts: the rewrite is invisible to the page, so
 * its Origin stays the one the engine allows, whatever listens on 1420.
 */
export const test = base.extend({
  context: async ({ context }, use) => {
    const web = process.env.DIALGA_E2E_WEB;
    if (!web) {
      throw new Error("DIALGA_E2E_WEB is not set: run the tests with pnpm run e2e");
    }
    await context.route(`${DEV_ORIGIN}/**`, (route) => {
      const url = new URL(route.request().url());
      return route.continue({ url: `${web}${url.pathname}${url.search}` });
    });
    await use(context);
  },
});
