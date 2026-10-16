import { FAKE_TARGET, removeConfig, writeConfig } from "./config";
import { expect, test } from "./fixtures";

test.beforeEach(async () => {
  await removeConfig();
});

test("walks the import wizard up to the first error: a file that is not there", async ({
  page,
}) => {
  await writeConfig(FAKE_TARGET);
  await page.goto("/");
  // From the card of the instance, like a user would.
  const card = page.getByRole("region", { name: "e2e-fake" });
  await card.getByRole("link", { name: "Import into e2e-fake" }).click();
  await expect(page).toHaveURL(/#import\/e2e-fake$/);
  await expect(page.getByRole("heading", { level: 1, name: "Import" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Import" })).toHaveAttribute("aria-current", "page");
  await expect(page.getByLabel("Instance")).toHaveValue("e2e-fake");

  // 1. Files: a relative path is refused in the page, before the engine.
  await expect(page.getByRole("heading", { level: 2, name: "Step 1 of 6: Files" })).toBeVisible();
  const next = page.getByRole("button", { name: "Next" });
  await expect(next).toBeDisabled();
  const paths = page.getByLabel("File paths");
  await paths.fill("backups/app_db.bak");
  await page.getByRole("button", { name: "Add files" }).click();
  await expect(paths).toHaveAttribute("aria-invalid", "true");
  await expect(page.getByText("backups/app_db.bak is not an absolute path")).toBeVisible();

  await paths.fill("/nonexistent/dialga-e2e/app_db.bak");
  await page.getByRole("button", { name: "Add files" }).click();
  await expect(page.getByText("app_db.bak", { exact: true })).toBeVisible();

  // 2. Verify: the engine checks the path before starting any container.
  await next.click();
  const verify = page.getByRole("heading", { level: 2, name: "Step 2 of 6: Verify" });
  await expect(verify).toBeVisible();
  await expect(verify).toBeFocused();
  await expect(page.getByRole("alert")).toContainText(
    "/nonexistent/dialga-e2e/app_db.bak: no such file or directory",
  );
  await expect(page.getByRole("button", { name: "Verify again" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Next" })).toBeDisabled();

  // Back to the files, the list is still there.
  await page.getByRole("button", { name: "Back", exact: true }).click();
  await expect(page.getByRole("heading", { level: 2, name: "Step 1 of 6: Files" })).toBeFocused();
  await expect(page.getByText("/nonexistent/dialga-e2e/app_db.bak")).toBeVisible();
});

// The inspection needs a container runtime and the job needs AWS: both are
// answered here, so the wizard runs from the files to its result on the
// real engine's page, with the job followed through its events.
const DEV_ORIGIN = "http://localhost:1420";
const PATH = "/data/backups/app_db.bak";
const JOB_ID = "20261016T093000Z-1a2b3c4d";
const GIB = 1024 ** 3;

const INSPECTED = {
  outcome: "restorable",
  runtime: "podman 5.8.4",
  image: "mcr.microsoft.com/mssql/server:2022-latest",
  server: "16.0.4135.4",
  target: {
    name: "e2e-fake",
    instance: "e2e-fake-instance",
    engine: "sqlserver-se",
    engineVersion: "14.00.3540.1.v1",
    major: 14,
    freeBytes: 100 * GIB,
  },
  backups: [
    {
      files: [PATH],
      outcome: "restorable",
      requiredBytes: 8 * GIB,
      backupSets: 1,
      header: {
        databaseName: "app_db",
        serverName: "SRC",
        backupType: "full",
        version: "13.0.5026",
        major: 13,
        product: "SQL Server 2016",
        compatibilityLevel: 130,
        collation: "SQL_Latin1_General_CP1_CI_AS",
        recoveryModel: "FULL",
        compressed: true,
        checksums: true,
        copyOnly: false,
        backupSize: 2 * GIB,
        compressedSize: GIB,
        startedAt: "2026-10-01T10:00:00",
        finishedAt: "2026-10-01T10:05:00",
      },
      findings: [{ check: "version", status: "ok", message: "13 <= 14" }],
    },
  ],
  requiredBytes: 8 * GIB,
  space: { check: "space", status: "ok", message: "8 GiB of 100 GiB free" },
};

const PLAN = {
  kind: "import",
  target: "e2e-fake",
  instance: "e2e-fake-instance",
  protected: false,
  ready: true,
  digest: "b".repeat(32),
  items: [
    {
      database: "app_db",
      file: PATH,
      key: "import/app_db.bak",
      ready: true,
      checks: [
        { step: "database", label: "Target database", status: "ok", detail: "app_db is new" },
      ],
      actions: [
        {
          id: "restore",
          title: "Restore app_db",
          statement: "exec msdb.dbo.rds_restore_database @restore_db_name = N'app_db';",
        },
      ],
      uploads: [
        {
          path: PATH,
          bucket: "e2e-fake-bucket",
          key: "import/app_db.bak",
          size: 1024,
          partSize: 1024,
          parts: 1,
          replaces: false,
        },
      ],
    },
  ],
  preview: "Dry run: import job on e2e-fake",
};

const snapshot = (state: "running" | "succeeded") => ({
  id: JOB_ID,
  kind: "import",
  target: "e2e-fake",
  state,
  items: [
    {
      name: "app_db",
      state,
      step: "restore",
      percent: state === "running" ? 40 : 100,
      steps: [
        { id: "upload", title: "Upload app_db.bak", status: "succeeded" },
        {
          id: "restore",
          title: "Restore app_db",
          status: state === "running" ? "running" : "succeeded",
        },
      ],
      task: {
        id: 7,
        type: "RESTORE_DB",
        lifecycle: state === "running" ? "IN_PROGRESS" : "SUCCESS",
        percent: state === "running" ? 40 : 100,
      },
    },
  ],
});

const REPORT = {
  target: "e2e-fake",
  instance: "e2e-fake-instance",
  ok: true,
  findings: [{ step: "database", label: "Database", status: "ok", detail: "ONLINE" }],
  database: {
    name: "app_db",
    state: "ONLINE",
    recoveryModel: "FULL",
    compatibilityLevel: 130,
    userAccess: "MULTI_USER",
    readOnly: false,
  },
  files: [],
  access: true,
  orphanUsers: [],
  windowsPrincipals: 0,
  objects: [],
};

const sse = (events: [string, unknown][]) =>
  events.map(([e, d]) => `event: ${e}\ndata: ${JSON.stringify(d)}\n\n`).join("");

test("imports a backup as one engine job and follows it to the result", async ({ page }) => {
  await writeConfig(FAKE_TARGET);
  const answer = (path: string, body: unknown, contentType = "application/json") =>
    page.route(`**${path}`, (route) =>
      route.request().method() === "OPTIONS"
        ? route.continue()
        : route.fulfill({
            status: 200,
            contentType,
            headers: { "Access-Control-Allow-Origin": DEV_ORIGIN },
            body: typeof body === "string" ? body : JSON.stringify(body),
          }),
    );
  await answer(
    "/api/v1/inspect",
    sse([
      ["result", INSPECTED],
      ["done", { ok: true }],
    ]),
    "text/event-stream",
  );
  const sent: Record<string, unknown>[] = [];
  await page.route("**/api/v1/jobs", (route) => {
    if (route.request().method() !== "POST") {
      return route.fallback();
    }
    const body = route.request().postDataJSON() as Record<string, unknown>;
    sent.push(body);
    return route.fulfill({
      status: body.dryRun ? 200 : 201,
      contentType: "application/json",
      headers: { "Access-Control-Allow-Origin": DEV_ORIGIN },
      body: JSON.stringify(body.dryRun ? PLAN : snapshot("running")),
    });
  });
  // Running at the first read, succeeded once the stream ended.
  let reads = 0;
  await page.route(`**/api/v1/jobs/${JOB_ID}`, (route) =>
    route.request().method() === "OPTIONS"
      ? route.continue()
      : route.fulfill({
          status: 200,
          contentType: "application/json",
          headers: { "Access-Control-Allow-Origin": DEV_ORIGIN },
          body: JSON.stringify(snapshot(reads++ === 0 ? "running" : "succeeded")),
        }),
  );
  await answer(
    `/api/v1/jobs/${JOB_ID}/events`,
    sse([
      ["step.started", { step: "restore" }],
      ["job.succeeded", {}],
    ]),
    "text/event-stream",
  );
  await answer("/api/v1/targets/e2e-fake/databases/app_db/report", REPORT);

  await page.goto("/#import/e2e-fake");
  await page.getByLabel("File paths").fill(PATH);
  await page.getByRole("button", { name: "Add files" }).click();
  await page.getByRole("button", { name: "Next" }).click();
  await expect(page.getByText("Restorable")).toBeVisible();
  await page.getByRole("button", { name: "Next" }).click();
  await page.getByRole("button", { name: "Preview" }).click();

  // The dry run of the job: upload and restore in one plan.
  await expect(page.getByText("s3://e2e-fake-bucket/import/app_db.bak")).toBeVisible();
  await page.getByRole("button", { name: "Start import" }).click();

  const result = page.getByRole("heading", { level: 2, name: "Step 6 of 6: Result" });
  await expect(result).toBeVisible();
  await expect(page.getByText("Import complete: 1 restored.")).toBeVisible();
  await expect(page.getByText("The database is online and the login can enter it.")).toBeVisible();
  expect(sent).toHaveLength(2);
  expect(sent[0]).toMatchObject({ kind: "import", target: "e2e-fake", dryRun: true });
  expect(sent[1]).toMatchObject({
    kind: "import",
    target: "e2e-fake",
    digest: PLAN.digest,
    items: [{ database: "app_db", file: PATH, key: "import/app_db.bak" }],
  });

  // The job stays in Activity.
  await page.getByRole("link", { name: "Follow it in Activity" }).click();
  await expect(page).toHaveURL(new RegExp(`#activity/${JOB_ID}$`));
});
