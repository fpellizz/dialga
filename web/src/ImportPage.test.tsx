import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { ImportPage } from "./ImportPage";
import type { InspectResult } from "./inspect";
import type { DatabaseReport, PlanPreview } from "./plans";
import { CONFIG_PATH, DEV, PROD, RESTORED, RESTORING, renderI18n } from "./test/fixtures";
import {
  ENGINE,
  type FetchRequest,
  json,
  mockFetch,
  requestsTo,
  sseStream,
  text,
} from "./test/http";
import type { UploadBatch } from "./uploads";

const GIB = 1024 ** 3;
const TARGETS = "GET /api/v1/targets";
const INSPECT = "POST /api/v1/inspect";
const UPLOADS = "POST /api/v1/targets/dev-sql/uploads";
const RESTORE = "POST /api/v1/targets/dev-sql/plans/restore";
const EXECUTE = "POST /api/v1/targets/dev-sql/plans/restore/execute";
const REPORT = "GET /api/v1/targets/dev-sql/databases/app_db/report";
const PATH = "/data/backups/app_db.bak";

/** A finished event stream: all the events, then the end. */
function events(...list: [string, unknown][]): Response {
  return new Response(
    list.map(([e, d]) => `event: ${e}\ndata: ${JSON.stringify(d)}\n\n`).join(""),
    {
      headers: { "Content-Type": "text/event-stream" },
    },
  );
}

function body(request: FetchRequest): Record<string, unknown> {
  return JSON.parse(String(request.init.body));
}

const RESTORABLE: InspectResult = {
  outcome: "restorable",
  runtime: "podman 5.8.4",
  image: "mcr.microsoft.com/mssql/server:2022-latest",
  server: "16.0.4135.4",
  target: {
    name: "dev-sql",
    instance: "my-sqlserver",
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

const NOT_RESTORABLE: InspectResult = {
  ...RESTORABLE,
  outcome: "not-restorable",
  backups: [
    {
      ...RESTORABLE.backups[0],
      outcome: "not-restorable",
      findings: [
        {
          check: "version",
          status: "failed",
          message: "SQL Server 2022 (16) is newer than the target (14)",
        },
      ],
      remedy: "Restore it on a SQL Server 2022 and export it again as .bacpac.",
    },
  ],
};

const BATCH: UploadBatch = {
  plan: {
    operation: "upload",
    target: "dev-sql",
    instance: "my-sqlserver",
    protected: false,
    actions: [],
  },
  confirmation: "",
  items: [
    {
      step: "upload-1",
      path: PATH,
      bucket: "my-sqlserver-backups",
      key: "import/app_db.bak",
      size: 1024,
      partSize: 1024,
      parts: 1,
      replaces: false,
    },
  ],
  rejected: [],
};

const PREVIEW: PlanPreview = {
  plan: {
    operation: "restore",
    target: "dev-sql",
    instance: "my-sqlserver",
    protected: false,
    actions: [
      {
        id: "restore",
        title: "Restore app_db from s3://my-sqlserver-backups/import/app_db.bak",
        kind: "sql",
        risk: "change",
        statement: "exec msdb.dbo.rds_restore_database ...",
      },
    ],
  },
  checks: [
    {
      step: "source",
      label: "Backup in S3",
      status: "failed",
      detail: "object not found: s3://my-sqlserver-backups/import/app_db.bak (upload it first)",
    },
    {
      step: "database",
      label: "Target database",
      status: "ok",
      detail: "app_db does not exist yet",
    },
  ],
  ready: false,
  digest: "digest-1",
  preview: "1. Restore app_db",
  request: { database: "app_db", key: "import/app_db.bak" },
};

const REPORT_OK: DatabaseReport = {
  target: "dev-sql",
  instance: "my-sqlserver",
  ok: true,
  findings: [
    {
      step: "database",
      label: "Database",
      status: "ok",
      detail: "ONLINE, compatibility level 130",
    },
    { step: "access", label: "Login access", status: "ok", detail: "admin can enter app_db" },
    {
      step: "orphans",
      label: "Orphaned users",
      status: "warning",
      detail:
        "1 SQL user(s) without a login on the instance: app_user. For each one: CREATE LOGIN [u] ...",
    },
  ],
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
  orphanUsers: ["app_user"],
  windowsPrincipals: 0,
  objects: [{ type: "USER_TABLE", count: 42 }],
};

function targets(...list: (typeof DEV)[]) {
  return () => json({ configPath: CONFIG_PATH, targets: list });
}

function open(hash = "#import/dev-sql") {
  window.location.hash = hash;
  renderI18n(<ImportPage engine={ENGINE} engineError="" />);
}

/** Files → Verify, with the given paths typed. */
async function addAndVerify(...paths: string[]) {
  await userEvent.type(await screen.findByLabelText("File paths"), paths.join("\n"));
  await userEvent.click(screen.getByRole("button", { name: "Add files" }));
  await userEvent.click(screen.getByRole("button", { name: "Next" }));
  expect(
    await screen.findByRole("heading", { level: 2, name: "Step 2 of 6: Verify" }),
  ).toHaveFocus();
}

/** Verify → Destination → Preview, with the defaults. */
async function toPreview() {
  await userEvent.click(await screen.findByRole("button", { name: "Next" }));
  await screen.findByRole("heading", { level: 2, name: "Step 3 of 6: Destination" });
  await userEvent.click(screen.getByRole("button", { name: "Preview" }));
  await screen.findByRole("heading", { level: 2, name: "Step 4 of 6: Preview" });
}

describe("ImportPage", () => {
  it("imports a backup end to end: verify, preview, upload, restore, report", async () => {
    const upload = sseStream();
    const restore = sseStream();
    const fetch = mockFetch({
      [TARGETS]: targets(DEV),
      [INSPECT]: () =>
        events(
          ["step", { step: "runtime", label: "Container runtime", status: "ok" }],
          ["result", RESTORABLE],
          ["done", { ok: true }],
        ),
      [UPLOADS]: (request) => (body(request).apply ? upload.response : json(BATCH)),
      [RESTORE]: () => json(PREVIEW),
      [EXECUTE]: () => restore.response,
      [REPORT]: () => json(REPORT_OK),
    });
    open();

    // 1. Files
    expect(
      await screen.findByRole("heading", { level: 2, name: "Step 1 of 6: Files" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();
    await addAndVerify(PATH);
    expect(body({ url: new URL(ENGINE.url), init: requestsTo(fetch, INSPECT)[0] })).toEqual({
      paths: [PATH],
      target: "dev-sql",
    });

    // 2. Verify
    const item = (await screen.findByText("Restorable")).closest("li") as HTMLElement;
    expect(
      within(item).getByText(/Database app_db · SQL Server 2016 \(13\.0\.5026\) · full backup/),
    ).toBeInTheDocument();
    expect(screen.getByText("Space needed: 8 GiB of 100 GiB free")).toBeInTheDocument();

    // 3. Destination
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(
      await screen.findByRole("heading", { level: 2, name: "Step 3 of 6: Destination" }),
    ).toHaveFocus();
    expect(screen.getByLabelText("Database name")).toHaveValue("app_db");
    expect(screen.getByLabelText("S3 folder")).toHaveValue("import");
    // The stepper goes back to a done step, and forward again with Next.
    await userEvent.click(screen.getByRole("button", { name: "Go back to Verify" }));
    expect(
      await screen.findByRole("heading", { level: 2, name: "Step 2 of 6: Verify" }),
    ).toHaveFocus();
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await screen.findByRole("heading", { level: 2, name: "Step 3 of 6: Destination" });
    expect(screen.getByText("s3://my-sqlserver-backups/import/app_db.bak")).toBeInTheDocument();

    // 4. Preview: the source check waits for the upload, the rest is ready.
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(await screen.findByText("Checked again after the upload.")).toBeInTheDocument();
    // The shared plan view names the action in the UI language, with the key.
    expect(screen.getByText("Restore app_db from import/app_db.bak")).toBeInTheDocument();
    expect(
      screen.getByText(/s3:\/\/my-sqlserver-backups\/import\/app_db\.bak · 1 parts/),
    ).toBeInTheDocument();
    const [planInit] = requestsTo(fetch, UPLOADS);
    expect(JSON.parse(String(planInit.body))).toEqual({
      files: [PATH],
      prefix: "import",
      apply: false,
    });
    expect(JSON.parse(String(requestsTo(fetch, RESTORE)[0].body))).toEqual({
      database: "app_db",
      key: "import/app_db.bak",
    });

    // 5. Progress: the upload, then the restore.
    await userEvent.click(screen.getByRole("button", { name: "Start import" }));
    await screen.findByRole("heading", { level: 2, name: "Step 5 of 6: Progress" });
    upload.send("progress", {
      path: PATH,
      key: "import/app_db.bak",
      done: 512,
      total: 1024,
      parts: 0,
      partsTotal: 1,
      bytesPerSecond: 1024 * 1024,
      etaSeconds: 65,
    });
    const bar = await screen.findByRole("progressbar", { name: "Upload of app_db.bak" });
    await waitFor(() => expect(bar).toHaveAttribute("aria-valuenow", "50"));
    expect(screen.getByText("512 B of 1 KiB · 1 MiB/s · 1:05 left")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel upload" })).toBeInTheDocument();
    // While it runs, the done steps cannot be reopened.
    expect(
      within(screen.getByRole("navigation", { name: "Steps" })).queryAllByRole("button"),
    ).toHaveLength(0);
    upload.send("done", { ok: true, results: [] });
    upload.close();

    expect(await screen.findByText("Uploaded and verified.")).toBeInTheDocument();
    restore.send("step", { step: "restore", label: "Restore app_db", status: "running" });
    restore.send("task", RESTORING);
    const task = await screen.findByRole("progressbar", { name: "RESTORE_DB sales" });
    expect(task).toHaveAttribute("aria-valuenow", "40");
    expect(screen.getByRole("button", { name: "Stop following" })).toBeInTheDocument();
    restore.send("step", { step: "restore", label: "Restore app_db", status: "ok" });
    restore.send("done", { ok: true, task: RESTORED });
    restore.close();
    const [executeInit] = requestsTo(fetch, EXECUTE);
    expect(JSON.parse(String(executeInit.body))).toEqual({
      database: "app_db",
      key: "import/app_db.bak",
      digest: "digest-1",
    });
    expect(JSON.parse(String(requestsTo(fetch, UPLOADS)[1].body))).toEqual({
      files: [PATH],
      prefix: "import",
      apply: true,
    });

    // 6. Result: the post report with the orphaned users.
    expect(
      await screen.findByRole("heading", { level: 2, name: "Step 6 of 6: Result" }),
    ).toHaveFocus();
    expect(screen.getByText("Import complete: 1 restored.")).toBeInTheDocument();
    const result = await screen.findByRole("region", { name: "Database app_db" });
    expect(
      await within(result).findByText("The database is online and the login can enter it."),
    ).toBeInTheDocument();
    expect(within(result).getByText("SQL users without a login: 1")).toBeInTheDocument();
    const orphans = result.querySelector(".orphan-list") as HTMLElement;
    expect(
      within(orphans)
        .getAllByRole("listitem")
        .map((li) => li.textContent),
    ).toEqual(["app_user"]);
    expect(
      within(result).getByText(/ALTER USER \[app_user\] WITH LOGIN = \[app_user\];/),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open Databases" })).toHaveAttribute(
      "href",
      "#databases/dev-sql",
    );

    await userEvent.click(screen.getByRole("button", { name: "Import more files" }));
    expect(
      await screen.findByRole("heading", { level: 2, name: "Step 1 of 6: Files" }),
    ).toHaveFocus();
  });

  it("stops at a backup that cannot be restored and says what to do", async () => {
    const fetch = mockFetch({
      [TARGETS]: targets(DEV),
      [INSPECT]: () => events(["result", NOT_RESTORABLE], ["done", { ok: false }]),
    });
    open();
    await addAndVerify(PATH);

    expect(await screen.findByText("Not restorable")).toBeInTheDocument();
    expect(screen.getByText("SQL Server 2022 (16) is newer than the target (14)")).toHaveClass(
      "field-error",
    );
    expect(
      screen.getByText("Restore it on a SQL Server 2022 and export it again as .bacpac."),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Remove the backups that cannot be restored to go on."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();

    // Removing the only backup goes back to the files.
    await userEvent.click(screen.getByRole("button", { name: "Remove from the import" }));
    expect(
      await screen.findByRole("heading", { level: 2, name: "Step 1 of 6: Files" }),
    ).toHaveFocus();
    expect(screen.queryByText("app_db.bak")).not.toBeInTheDocument();
    expect(requestsTo(fetch, INSPECT)).toHaveLength(1);
  });

  it("refuses a relative path and shows why the inspection failed", async () => {
    mockFetch({
      [TARGETS]: targets(DEV),
      [INSPECT]: () => text("/data/missing.bak: no such file or directory\n", 400),
    });
    open();
    await userEvent.type(await screen.findByLabelText("File paths"), "backups/app.bak");
    await userEvent.click(screen.getByRole("button", { name: "Add files" }));
    const paths = screen.getByLabelText("File paths");
    expect(paths).toHaveAttribute("aria-invalid", "true");
    expect(paths).toHaveAccessibleDescription(expect.stringContaining("is not an absolute path"));

    await userEvent.clear(paths);
    await addAndVerify("/data/missing.bak");
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "/data/missing.bak: no such file or directory",
    );
    expect(screen.getByRole("button", { name: "Verify again" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();
  });

  it("goes back to the preview when the plan changed before the restore (412)", async () => {
    const fetch = mockFetch({
      [TARGETS]: targets(DEV),
      [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
      [UPLOADS]: (request) =>
        body(request).apply ? events(["done", { ok: true, results: [] }]) : json(BATCH),
      [RESTORE]: () => json(PREVIEW),
      [EXECUTE]: () => text("the plan changed since the preview: preview it again\n", 412),
    });
    open();
    await addAndVerify(PATH);
    await toPreview();
    await userEvent.click(await screen.findByRole("button", { name: "Start import" }));

    expect(
      await screen.findByText("the plan changed since the preview: preview it again"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("The instance changed since the preview: preview again before going on."),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Back to the preview" }));
    expect(
      await screen.findByRole("heading", { level: 2, name: "Step 4 of 6: Preview" }),
    ).toHaveFocus();
    await screen.findByText("Checked again after the upload.");
    expect(requestsTo(fetch, RESTORE)).toHaveLength(2);
  });

  it("says the engine cannot upload yet and does not start", async () => {
    mockFetch({
      [TARGETS]: targets(DEV),
      [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
      [UPLOADS]: () => text("404 page not found\n", 404),
      [RESTORE]: () => json(PREVIEW),
    });
    open();
    await addAndVerify(PATH);
    await toPreview();
    expect(
      await screen.findByText(
        "This engine cannot upload files yet: the upload arrives with the transfer feature.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start import" })).toBeDisabled();
  });

  it("asks to retype the name of a protected instance before it starts", async () => {
    const prodBatch = {
      ...BATCH,
      confirmation: "prod-sql",
      plan: { ...BATCH.plan, target: "prod-sql", protected: true },
    };
    const prodPreview = { ...PREVIEW, confirmation: "prod-sql", checks: [PREVIEW.checks[0]] };
    mockFetch({
      [TARGETS]: targets(DEV, PROD),
      [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
      "POST /api/v1/targets/prod-sql/uploads": () => json(prodBatch),
      "POST /api/v1/targets/prod-sql/plans/restore": () => json(prodPreview),
    });
    open("#import/prod-sql");
    expect(await screen.findByLabelText("Instance")).toHaveValue("prod-sql");
    await addAndVerify(PATH);
    await toPreview();

    const start = await screen.findByRole("button", { name: "Start import" });
    expect(start).toBeDisabled();
    const confirm = screen.getByLabelText("Type prod-sql to confirm");
    expect(confirm).toHaveAccessibleDescription(
      "The instance is protected: every change asks to retype its name.",
    );
    await userEvent.type(confirm, "prod");
    expect(start).toBeDisabled();
    await userEvent.type(confirm, "-sql");
    expect(start).toBeEnabled();
  });

  it("asks to retype what the upload replaces", async () => {
    const replacing = { ...BATCH, confirmation: "import/app_db.bak" };
    mockFetch({
      [TARGETS]: targets(DEV),
      [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
      [UPLOADS]: () => json(replacing),
      [RESTORE]: () => json(PREVIEW),
    });
    open();
    await addAndVerify(PATH);
    await toPreview();
    const confirm = await screen.findByLabelText("Type import/app_db.bak to confirm");
    expect(confirm).toHaveAccessibleDescription(
      "The upload replaces objects already in the bucket: retype what it replaces to confirm.",
    );
  });

  it("speaks Italian", async () => {
    mockFetch({ [TARGETS]: targets(DEV) });
    window.location.hash = "#import/dev-sql";
    renderI18n(<ImportPage engine={ENGINE} engineError="" />, "it");
    // The page title shows while the instances load; the wizard comes with them.
    expect(
      await screen.findByRole("heading", { level: 2, name: "Passo 1 di 6: File" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: "Importa" })).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Passi" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Avanti" })).toBeDisabled();
    expect(screen.getByLabelText("Percorsi dei file")).toBeInTheDocument();
  });
});
