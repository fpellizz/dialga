import type { Page } from "@playwright/test";
import { FAKE_TARGET, removeConfig, writeConfig } from "./config";
import { expect, test } from "./fixtures";

// The fake target has no SQL password: the engine refuses every SQL call
// with 422. The list of databases is answered here instead, so a row exists
// to open the dialogs on; the plans themselves go to the real engine.
const DEV_ORIGIN = "http://localhost:1420";

const SALES = {
  name: "sales",
  state: "ONLINE",
  recovery: "FULL",
  compatibility: 140,
  owner: "rdsa",
  userAccess: "MULTI_USER",
  readOnly: false,
  createdAt: "2026-10-15T08:00:00",
  dataMB: 8,
  logMB: 2,
};

const DROP_PREVIEW = {
  plan: {
    operation: "drop",
    target: "e2e-fake",
    instance: "e2e-fake-instance",
    protected: false,
    subject: "sales",
    actions: [
      {
        id: "drop",
        title: "Drop database sales",
        kind: "sql",
        risk: "destructive",
        statement: "exec msdb.dbo.rds_drop_database @db_name = N'sales';",
      },
    ],
  },
  checks: [{ step: "database", label: "Database", status: "ok", detail: "sales is ONLINE" }],
  ready: true,
  confirmation: "sales",
  digest: "d".repeat(32),
  preview: "Dry run: drop on e2e-fake",
  request: { database: "sales" },
};

/** Answers an engine route from the test, with the CORS header the engine would send. */
async function answer(page: Page, path: string, body: unknown) {
  await page.route(`**${path}`, (route) =>
    route.request().method() === "OPTIONS"
      ? route.continue()
      : route.fulfill({
          status: 200,
          contentType: "application/json",
          headers: { "Access-Control-Allow-Origin": DEV_ORIGIN },
          body: JSON.stringify(body),
        }),
  );
}

async function focusInDialog(page: Page): Promise<boolean> {
  return page.evaluate(() => !!document.querySelector("dialog")?.contains(document.activeElement));
}

test.beforeEach(async ({ page }) => {
  await removeConfig();
  await writeConfig(FAKE_TARGET);
  await answer(page, "/api/v1/targets/e2e-fake/databases", { databases: [SALES] });
});

test("explains why the engine cannot preview a drop", async ({ page }) => {
  await page.goto("/#databases/e2e-fake");
  const drop = page.getByRole("button", { name: "Drop sales" });
  await drop.click();

  const dialog = page.getByRole("dialog", { name: "Drop sales" });
  await expect(dialog).toBeVisible();
  // 428 from the real engine: no SQL password yet.
  const alert = dialog.getByRole("alert");
  await expect(alert).toContainText("The SQL password for e2e-fake is needed");
  await expect(dialog.getByRole("textbox", { name: /to confirm$/ })).toHaveCount(0);
  await expect(dialog.getByRole("button", { name: "Drop database" })).toBeDisabled();

  // Modal: the focus stays inside, Escape closes, the focus goes back.
  for (const key of ["Tab", "Tab", "Tab", "Tab", "Tab", "Shift+Tab", "Shift+Tab", "Shift+Tab"]) {
    await page.keyboard.press(key);
    expect(await focusInDialog(page), key).toBe(true);
  }
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(drop).toBeFocused();
});

test("runs a drop only on the exact name, then shows the engine refusal", async ({ page }) => {
  await answer(page, "/api/v1/targets/e2e-fake/plans/drop", DROP_PREVIEW);
  await page.goto("/#databases/e2e-fake");
  await page.getByRole("button", { name: "Drop sales" }).click();

  const dialog = page.getByRole("dialog", { name: "Drop sales" });
  const confirm = dialog.getByRole("textbox", { name: "Type sales to confirm" });
  await expect(confirm).toBeFocused();
  await expect(dialog.getByText("Destructive")).toBeVisible();
  await dialog.getByText("Details").click();
  await expect(dialog.locator("pre.statement")).toHaveText(
    "exec msdb.dbo.rds_drop_database @db_name = N'sales';",
  );

  const run = dialog.getByRole("button", { name: "Drop database" });
  await expect(run).toBeDisabled();
  await confirm.fill("Sales");
  await expect(run).toBeDisabled();
  await confirm.fill("sales");
  await expect(run).toBeEnabled();

  // The execution goes to the real engine, which refuses it before running anything.
  await run.click();
  await expect(dialog.getByRole("alert")).toContainText("The SQL password for e2e-fake is needed");
  await expect(run).toBeVisible();
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toHaveCount(0);
});

test("asks for the restore values before previewing", async ({ page }) => {
  await page.goto("/#databases/e2e-fake");
  await page.getByRole("button", { name: "Restore from S3" }).click();
  const dialog = page.getByRole("dialog", { name: "Restore from S3" });
  const preview = dialog.getByRole("button", { name: "Preview" });
  await expect(dialog.getByRole("textbox", { name: "Database name" })).toBeFocused();
  await expect(preview).toBeDisabled();
  await dialog.getByRole("textbox", { name: "Database name" }).fill("sales_copy");
  await dialog.getByRole("textbox", { name: "S3 key of the backup" }).fill("sales.bak");
  await dialog.getByRole("textbox", { name: "S3 key of the backup" }).press("Enter");
  await expect(dialog.getByRole("alert")).toContainText("The SQL password for e2e-fake is needed");
});
