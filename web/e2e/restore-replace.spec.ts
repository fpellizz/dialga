import type { Page } from "@playwright/test";
import { FAKE_TARGET, removeConfig, writeConfig } from "./config";
import { expect, test } from "./fixtures";

// Restore over an existing database. As in operations.spec.ts the list of
// databases and the plans are answered here (the fake target has no SQL
// password); the page is the real one.
const DEV_ORIGIN = "http://localhost:1420";
const OLD = "sales_old_20261016T100000";

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

const SWAP_PREVIEW = {
  plan: {
    operation: "restore",
    target: "e2e-fake",
    instance: "e2e-fake-instance",
    protected: false,
    actions: [
      {
        id: "restore",
        title: "Restore sales_dialga_new_20261016T100000",
        kind: "sql",
        risk: "change",
        statement: "exec msdb.dbo.rds_restore_database ...",
      },
      {
        id: "rename",
        title: `Rename sales to ${OLD}`,
        kind: "sql",
        risk: "destructive",
        statement: `ALTER DATABASE [sales] MODIFY NAME = [${OLD}];`,
      },
    ],
  },
  checks: [
    { step: "database", label: "Target database", status: "ok", detail: "sales exists" },
    {
      step: "replace",
      label: "Replace",
      status: "ok",
      detail: "swap: two copies (2 × 42.0 GiB) fit in 97.2 GiB free",
    },
    {
      step: "sessions",
      label: "Active sessions",
      status: "warning",
      detail: "3 sessions on sales",
    },
  ],
  ready: true,
  confirmation: "sales",
  digest: "r".repeat(32),
  preview: "Dry run: restore on e2e-fake",
  request: { database: "sales", key: "import/sales.bak", replace: "swap" },
};

const EXECUTION = [
  ["step", { step: "swap", label: "Swap the databases", status: "ok", detail: `kept as ${OLD}` }],
  ["done", { ok: true }],
]
  .map(([event, data]) => `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`)
  .join("");

/** Answers an engine route, keeping the bodies it received. */
async function answer(page: Page, path: string, body: unknown, type = "application/json") {
  const received: unknown[] = [];
  await page.route(`**${path}`, (route) => {
    if (route.request().method() === "OPTIONS") {
      return route.continue();
    }
    received.push(route.request().postDataJSON());
    return route.fulfill({
      status: 200,
      contentType: type,
      headers: { "Access-Control-Allow-Origin": DEV_ORIGIN },
      body: typeof body === "string" ? body : JSON.stringify(body),
    });
  });
  return received;
}

test.beforeEach(async ({ page }) => {
  await removeConfig();
  await writeConfig(FAKE_TARGET);
  await answer(page, "/api/v1/targets/e2e-fake/databases", { databases: [SALES] });
});

test("restores over an existing database by swapping, then offers to drop the old one", async ({
  page,
}) => {
  const previews = await answer(page, "/api/v1/targets/e2e-fake/plans/restore", SWAP_PREVIEW);
  const executions = await answer(
    page,
    "/api/v1/targets/e2e-fake/plans/restore/execute",
    EXECUTION,
    "text/event-stream",
  );
  await answer(page, "/api/v1/targets/e2e-fake/plans/drop", {
    ...SWAP_PREVIEW,
    plan: { ...SWAP_PREVIEW.plan, operation: "drop", actions: [] },
    checks: [],
    confirmation: OLD,
    request: { database: OLD },
  });

  await page.goto("/#databases/e2e-fake");
  await page.getByRole("button", { name: "Restore from S3" }).click();
  const dialog = page.getByRole("dialog", { name: "Restore from S3" });
  await dialog.getByRole("textbox", { name: "Database name" }).fill("sales");
  await dialog.getByRole("textbox", { name: "S3 key of the backup" }).fill("import/sales.bak");

  // The name exists: the choice appears, stopping is the default.
  const group = dialog.getByRole("radiogroup", { name: "The database sales already exists" });
  await expect(group.getByRole("radio", { name: "Stop" })).toBeChecked();
  await group.getByRole("radio", { name: "Keep the old one (swap)" }).check();
  await dialog.getByRole("button", { name: "Preview" }).click();

  const checks = dialog.getByRole("region", { name: "Pre-flight checks" });
  await expect(checks.getByText("Replace the existing database")).toBeVisible();
  await expect(checks.getByText(/swap: two copies/)).toBeVisible();
  expect(previews).toEqual([{ database: "sales", key: "import/sales.bak", replace: "swap" }]);

  // Any replace retypes the database name.
  const run = dialog.getByRole("button", { name: "Restore", exact: true });
  await expect(run).toBeDisabled();
  await dialog.getByRole("textbox", { name: "Type sales to confirm" }).fill("sales");
  await run.click();
  expect(executions).toHaveLength(1);
  expect(executions[0]).toMatchObject({ database: "sales", replace: "swap", confirm: "sales" });

  const outcome = dialog.getByRole("region", { name: "Result of the replace" });
  await expect(outcome).toContainText(`The previous database was kept as ${OLD}.`);
  await outcome.getByRole("button", { name: `Drop ${OLD}` }).click();
  await expect(page.getByRole("dialog", { name: `Drop ${OLD}` })).toBeVisible();
});

test("warns when a drop would not back the database up first", async ({ page }) => {
  await page.goto("/#databases/e2e-fake");
  await page.getByRole("button", { name: "Restore from S3" }).click();
  const dialog = page.getByRole("dialog", { name: "Restore from S3" });
  await dialog.getByRole("textbox", { name: "Database name" }).fill("sales");
  await dialog.getByRole("radio", { name: "Drop it and restore" }).check();
  const backup = dialog.getByRole("checkbox", { name: "Back up the current database to S3 first" });
  await expect(backup).toBeChecked();
  await expect(dialog.getByRole("alert")).toHaveCount(0);
  await backup.uncheck();
  await expect(dialog.getByRole("alert")).toContainText(
    "Without the backup, sales is lost for good",
  );
});
