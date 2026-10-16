import type { Page } from "@playwright/test";
import { FAKE_TARGET, removeConfig, writeConfig } from "./config";
import { expect, test } from "./fixtures";

// The SQL password flow without a SQL Server: the engine's answers are the
// mocks below (the targets list with the password state, the 428 of the
// databases and the PUT of the password), as the engine of this contract sends
// them. The Access-Control-Expose-Headers of the 428 is left out on purpose:
// the page must find the target in the text too.
const DEV_ORIGIN = "http://localhost:1420";
const CORS = {
  "Access-Control-Allow-Origin": DEV_ORIGIN,
  "Access-Control-Allow-Headers": "Authorization, Content-Type",
  "Access-Control-Allow-Methods": "GET, POST, PUT, DELETE, OPTIONS",
};

const db = (name: string) => ({
  name,
  state: "ONLINE",
  recovery: "FULL",
  compatibility: 140,
  owner: "rdsa",
  userAccess: "MULTI_USER",
  readOnly: false,
  createdAt: "2026-10-15T08:00:00",
  dataMB: 8,
  logMB: 2,
});

const target = (available: boolean) => ({
  name: "e2e-fake",
  instance: "e2e-fake-instance",
  region: "eu-west-1",
  profile: "dialga-e2e-missing",
  accountId: "123456789012",
  bucket: "e2e-fake-bucket",
  sqlUser: "e2e",
  connectMode: "external",
  localPort: 11433,
  protected: false,
  importPrefix: "import",
  exportPrefix: "export",
  password: { source: "prompt", available, canRemember: false },
});

/** The engine as the contract describes it: the password is needed until a PUT is accepted. */
async function serveEngine(page: Page, login: { status: number; body: string }) {
  const state = { saved: false, bodies: [] as unknown[] };
  await page.route("**/api/v1/targets", (route) =>
    route.request().method() === "OPTIONS"
      ? route.fulfill({ status: 204, headers: CORS })
      : route.fulfill({
          contentType: "application/json",
          headers: CORS,
          body: JSON.stringify({ configPath: "e2e", targets: [target(state.saved)] }),
        }),
  );
  await page.route("**/api/v1/targets/e2e-fake/databases", (route) =>
    route.request().method() === "OPTIONS"
      ? route.fulfill({ status: 204, headers: CORS })
      : state.saved
        ? route.fulfill({
            contentType: "application/json",
            headers: CORS,
            body: JSON.stringify({ databases: [db("sales")] }),
          })
        : route.fulfill({
            status: 428,
            contentType: "text/plain; charset=utf-8",
            headers: CORS,
            body: "SQL password needed for target e2e-fake\n",
          }),
  );
  await page.route("**/api/v1/targets/e2e-fake/password?verify=1", (route) => {
    const request = route.request();
    if (request.method() === "OPTIONS") {
      return route.fulfill({ status: 204, headers: CORS });
    }
    state.bodies.push(request.postDataJSON());
    if (login.status === 204) {
      state.saved = true;
    }
    return route.fulfill({
      status: login.status,
      contentType: "text/plain; charset=utf-8",
      headers: CORS,
      body: login.body,
    });
  });
  return state;
}

test.beforeEach(async () => {
  await removeConfig();
  await writeConfig(FAKE_TARGET);
});

test("the Databases page asks for the SQL password, then lists the databases", async ({ page }) => {
  const engine = await serveEngine(page, { status: 204, body: "" });
  await page.goto("/#databases/e2e-fake");

  const banner = page.getByRole("alert");
  await expect(banner).toContainText("The SQL password for e2e-fake is needed");
  await banner.getByRole("button", { name: "Enter password for e2e-fake" }).click();

  const dialog = page.getByRole("dialog", { name: "SQL password for e2e-fake" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText("SQL user: e2e")).toBeVisible();
  const field = dialog.getByLabel("Password", { exact: true });
  await expect(field).toBeFocused();
  await expect(field).toHaveAttribute("type", "password");
  await expect(
    dialog.getByRole("checkbox", { name: "Remember in the system keyring" }),
  ).toBeDisabled();

  await field.fill("not-a-real-password");
  await dialog.getByRole("button", { name: "Connect" }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("table")).toContainText("sales");
  await expect(page.getByRole("alert")).toHaveCount(0);
  expect(engine.bodies).toEqual([{ password: "not-a-real-password", remember: false }]);
  // Nowhere in the page, nor in its address.
  expect(page.url()).not.toContain("not-a-real-password");
  await expect(page.locator("body")).not.toContainText("not-a-real-password");
});

test("a refused login stays in the dialog with SQL Server's message", async ({ page }) => {
  await serveEngine(page, { status: 422, body: "Login failed for user 'e2e'.\n" });
  await page.goto("/#databases/e2e-fake");
  await page.getByRole("button", { name: "Enter password for e2e-fake" }).click();
  const dialog = page.getByRole("dialog", { name: "SQL password for e2e-fake" });
  await dialog.getByLabel("Password", { exact: true }).fill("wrong");
  await dialog.getByRole("button", { name: "Connect" }).click();
  await expect(dialog.getByRole("alert")).toContainText("Login failed for user 'e2e'.");
  await expect(dialog.getByRole("alert")).toContainText("check the password and the SQL user");
  await expect(dialog.getByLabel("Password", { exact: true })).toBeFocused();

  // Escape closes it and the focus goes back to the button that opened it.
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("button", { name: "Enter password for e2e-fake" })).toBeFocused();
});
