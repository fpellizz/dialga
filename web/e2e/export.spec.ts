import type { Page } from "@playwright/test";
import { FAKE_TARGET, removeConfig, writeConfig } from "./config";
import { expect, test } from "./fixtures";

// The fake target has no SQL password and its AWS profile does not exist:
// the real engine answers the overview with 502 (the windows step shows
// it). The databases, the previews, the execution and the download are
// answered here, so the wizard runs to its result without any AWS call.
const DEV_ORIGIN = "http://localhost:1420";
const KEY = "export/sales_20261016-080000.bak";

const db = (name: string, state = "ONLINE") => ({
  name,
  state,
  recovery: "FULL",
  compatibility: 140,
  owner: "rdsa",
  userAccess: "MULTI_USER",
  readOnly: false,
  createdAt: "2026-10-15T08:00:00",
  dataMB: 8,
  logMB: 2,
});

const PREVIEW = {
  plan: {
    operation: "export",
    target: "e2e-fake",
    instance: "e2e-fake-instance",
    protected: false,
    subject: "sales",
    actions: [
      {
        id: "export",
        title: `Back up sales to s3://e2e-fake-bucket/${KEY}`,
        kind: "sql",
        risk: "change",
        statement: `exec msdb.dbo.rds_backup_database @source_db_name = N'sales', @s3_arn_to_backup_to = N'arn:aws:s3:::e2e-fake-bucket/${KEY}';`,
      },
    ],
  },
  checks: [
    { step: "database", label: "Database", status: "ok", detail: "sales is ONLINE" },
    {
      step: "destination",
      label: "Destination in S3",
      status: "ok",
      detail: `s3://e2e-fake-bucket/${KEY} is free`,
    },
    { step: "tasks", label: "Native tasks", status: "ok", detail: "no native task running" },
  ],
  ready: true,
  digest: "e".repeat(32),
  preview: "Dry run: export on e2e-fake",
  request: { database: "sales", key: KEY },
};

const TASK = {
  id: 31,
  type: "BACKUP_DB",
  database: "sales",
  percent: 100,
  lifecycle: "SUCCESS",
  s3Object: `arn:aws:s3:::e2e-fake-bucket/${KEY}`,
};

const sse = (events: [string, unknown][]) =>
  events.map(([e, d]) => `event: ${e}\ndata: ${JSON.stringify(d)}\n\n`).join("");

/** Answers an engine route from the test, with the CORS header the engine would send. */
async function answer(page: Page, path: string, body: string, contentType = "application/json") {
  await page.route(`**${path}`, (route) =>
    route.request().method() === "OPTIONS"
      ? route.continue()
      : route.fulfill({
          status: 200,
          contentType,
          headers: { "Access-Control-Allow-Origin": DEV_ORIGIN },
          body,
        }),
  );
}

test.beforeEach(async ({ page }) => {
  await removeConfig();
  await writeConfig(FAKE_TARGET);
  await answer(
    page,
    "/api/v1/targets/e2e-fake/databases",
    JSON.stringify({ databases: [db("sales"), db("staged", "RESTORING")] }),
  );
});

test("exports a database from the Databases page to S3 and a local folder", async ({ page }) => {
  await answer(page, "/api/v1/targets/e2e-fake/plans/export", JSON.stringify(PREVIEW));
  await answer(
    page,
    "/api/v1/targets/e2e-fake/plans/export/execute",
    sse([
      ["step", { step: "export", label: "Back up", status: "ok", detail: "task 31 created" }],
      ["task", { ...TASK, percent: 40, lifecycle: "IN_PROGRESS" }],
      ["task", TASK],
      ["done", { ok: true, task: TASK }],
    ]),
    "text/event-stream",
  );
  await answer(
    page,
    "/api/v1/targets/e2e-fake/downloads",
    sse([
      ["plan", { confirmation: "", items: [], rejected: [] }],
      ["progress", { path: "", key: KEY, done: 5, total: 10, bytesPerSecond: 5 }],
      [
        "done",
        {
          ok: true,
          results: [
            {
              path: "/tmp/e2e-exports/sales_20261016-080000.bak",
              bucket: "e2e-fake-bucket",
              key: KEY,
              size: 10485760,
              checksumAlgorithm: "SHA256",
              checksum: "c2hhMjU2LW9mLXRoZS1lMmUtYmFja3Vw",
              verified: true,
              seconds: 2,
              bytesPerSecond: 5242880,
            },
          ],
        },
      ],
    ]),
    "text/event-stream",
  );

  // From the Databases page: tick sales, Export selected.
  await page.goto("/#databases/e2e-fake");
  await page.getByRole("checkbox", { name: "Select sales" }).check();
  await page.getByRole("button", { name: "Export selected (1)" }).click();
  await expect(page).toHaveURL(/#export\/e2e-fake\?db=sales$/);
  await expect(page.getByRole("link", { name: "Export" })).toHaveAttribute("aria-current", "page");

  const step = (name: string) => page.getByRole("region", { name });
  await expect(
    step("Step 1 of 6: Databases").getByRole("checkbox", { name: /sales/ }),
  ).toBeChecked();
  await page.getByRole("button", { name: "Next" }).click();

  // Options: the keyboard reaches the download folder, which must be absolute.
  await expect(page.getByRole("heading", { name: "Step 2 of 6: Options" })).toBeFocused();
  await page.getByRole("checkbox", { name: /Download each backup/ }).check();
  await page.getByRole("textbox", { name: "Folder" }).fill("exports");
  await page.getByRole("button", { name: "Next" }).click();
  await expect(page.getByRole("alert")).toHaveText("Fix the fields marked as invalid.");
  await expect(page.getByRole("textbox", { name: "Folder" })).toBeFocused();
  await page.keyboard.press("Control+a");
  await page.keyboard.type("/tmp/e2e-exports");
  await page.getByRole("button", { name: "Next" }).click();

  // Windows: the real engine cannot read the instance (no such AWS profile).
  const windows = step("Step 3 of 6: Windows");
  await expect(windows.getByRole("alert")).toContainText("dialga-e2e-missing");
  await expect(windows.getByRole("alert")).toContainText("Create the profile dialga-e2e-missing");
  await page.getByRole("button", { name: "Next" }).click();

  // Preview, then run.
  const preview = step("Step 4 of 6: Preview");
  await expect(preview.getByText(`s3://e2e-fake-bucket/${KEY}`, { exact: true })).toBeVisible();
  await expect(preview.getByText("/tmp/e2e-exports/sales_20261016-080000.bak")).toBeVisible();
  await preview.getByText("T-SQL").click();
  await expect(preview.locator("pre.statement")).toContainText("rds_backup_database");
  await page.getByRole("button", { name: "Run export" }).click();

  // Result: S3, the local file and its verified checksum.
  const result = step("Step 6 of 6: Result");
  await expect(result.getByText("Exported: 1 · failed: 0 · not completed: 0")).toBeVisible();
  await expect(result.getByText(`s3://e2e-fake-bucket/${KEY}`)).toBeVisible();
  await expect(result.getByText("/tmp/e2e-exports/sales_20261016-080000.bak")).toBeVisible();
  await expect(result.getByText("Verified")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Step 6 of 6: Result" })).toBeFocused();
  // The earlier steps are not offered again after a run.
  await expect(page.getByRole("navigation", { name: "Steps" }).getByRole("button")).toHaveCount(0);
});

test("shows the engine refusal of the preview with what to do", async ({ page }) => {
  // The previews go to the real engine: no SQL password for e2e-fake (422).
  await page.goto("/#export/e2e-fake?db=sales");
  await page.getByRole("button", { name: "Next" }).click();
  await page.getByRole("button", { name: "Next" }).click();
  await page.getByRole("button", { name: "Next" }).click();
  const preview = page.getByRole("region", { name: "Step 4 of 6: Preview" });
  await expect(preview.getByRole("alert")).toContainText("SQL password:");
  await expect(page.getByRole("button", { name: "Run export" })).toBeDisabled();
  // Back to the databases from the stepper, by keyboard.
  await page.getByRole("button", { name: "Go back to Databases" }).focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { name: "Step 1 of 6: Databases" })).toBeFocused();
});
