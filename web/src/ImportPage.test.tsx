import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { JobPlan } from "./exportJob";
import { ImportPage } from "./ImportPage";
import type { InspectResult } from "./inspect";
import type { DatabaseReport } from "./plans";
import { CONFIG_PATH, DEV, database, PROD, renderI18n } from "./test/fixtures";
import {
  ENGINE,
  type FetchRequest,
  json,
  mockFetch,
  type Route,
  requestsTo,
  sseStream,
  text,
} from "./test/http";

const GIB = 1024 ** 3;
const TARGETS = "GET /api/v1/targets";
const INSPECT = "POST /api/v1/inspect";
const JOBS = "POST /api/v1/jobs";
const JOB_ID = "20261016T093000Z-1a2b3c4d";
const JOB = `GET /api/v1/jobs/${JOB_ID}`;
const EVENTS = `GET /api/v1/jobs/${JOB_ID}/events`;
const REPORT = "GET /api/v1/targets/dev-sql/databases/app_db/report";
const PATH = "/data/backups/app_db.bak";

/** The databases of a target: none, unless the test lists them (the Destination step reads them). */
function mockWithDatabases(routes: Record<string, Route>, existing: string[] = []) {
  const list = () => json({ databases: existing.map((name) => database(name)) });
  return mockFetch({
    "GET /api/v1/targets/dev-sql/databases": list,
    "GET /api/v1/targets/prod-sql/databases": list,
    ...routes,
  });
}

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

const PLAN: JobPlan = {
  kind: "import",
  target: "dev-sql",
  instance: "my-sqlserver",
  protected: false,
  ready: true,
  digest: "digest-1",
  items: [
    {
      database: "app_db",
      file: PATH,
      key: "import/app_db.bak",
      ready: true,
      checks: [
        {
          step: "database",
          label: "Target database",
          status: "ok",
          detail: "app_db does not exist yet",
        },
      ],
      actions: [
        {
          id: "restore",
          title: "Restore app_db from s3://my-sqlserver-backups/import/app_db.bak",
          statement: "exec msdb.dbo.rds_restore_database ...",
        },
      ],
      uploads: [
        {
          path: PATH,
          bucket: "my-sqlserver-backups",
          key: "import/app_db.bak",
          size: 1024,
          partSize: 1024,
          parts: 1,
          replaces: false,
        },
      ],
    },
  ],
  preview: "1. Upload app_db.bak\n2. Restore app_db",
};

/** The job as the engine reports it: the upload, then the restore and its native task. */
function snapshot(state: "running" | "succeeded" | "failed", progress: "upload" | "restore") {
  const item = {
    name: "app_db",
    state,
    step: progress,
    percent: state === "running" ? 50 : 100,
    steps: [
      {
        id: "upload",
        title: "Upload app_db.bak",
        status: progress === "upload" ? "running" : "succeeded",
      },
      {
        id: "restore",
        title: "Restore app_db",
        status: state === "running" ? "pending" : state === "failed" ? "failed" : "succeeded",
      },
    ],
    ...(progress === "upload"
      ? {
          transfer: {
            direction: "upload",
            bytes: 512,
            total: 1024,
            bytesPerSecond: 1024 * 1024,
            etaSeconds: 65,
          },
        }
      : {
          task: {
            id: 7,
            type: "RESTORE_DB",
            lifecycle: state === "running" ? "IN_PROGRESS" : "SUCCESS",
            percent: state === "running" ? 40 : 100,
          },
        }),
    ...(state === "failed" ? { error: "RDS task 7 ended in ERROR" } : {}),
  };
  return { id: JOB_ID, kind: "import", target: "dev-sql", state, items: [item] };
}

/** The dry run answers the plan, the start the job (201). */
function jobs(plan: unknown = PLAN, started: unknown = snapshot("running", "upload")) {
  return (request: FetchRequest) => (body(request).dryRun ? json(plan) : json(started, 201));
}

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
  await waitFor(() =>
    expect(screen.getByRole("heading", { level: 2, name: "Step 2 of 6: Verify" })).toHaveFocus(),
  );
}

/** Verify → Destination → Preview, with the defaults. */
async function toPreview() {
  await userEvent.click(await screen.findByRole("button", { name: "Next" }));
  await screen.findByRole("heading", { level: 2, name: "Step 3 of 6: Destination" });
  await userEvent.click(screen.getByRole("button", { name: "Preview" }));
  await screen.findByRole("heading", { level: 2, name: "Step 4 of 6: Preview" });
}

describe("ImportPage", () => {
  it("imports a backup end to end: verify, preview, job, report", async () => {
    const stream = sseStream();
    let reads = 0;
    const fetch = mockWithDatabases({
      [TARGETS]: targets(DEV),
      [INSPECT]: () =>
        events(
          ["step", { step: "runtime", label: "Container runtime", status: "ok" }],
          ["result", RESTORABLE],
          ["done", { ok: true }],
        ),
      [JOBS]: jobs(),
      [EVENTS]: () => stream.response,
      [JOB]: () =>
        json(reads++ === 0 ? snapshot("running", "restore") : snapshot("succeeded", "restore")),
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
    await waitFor(() =>
      expect(
        screen.getByRole("heading", { level: 2, name: "Step 3 of 6: Destination" }),
      ).toHaveFocus(),
    );
    expect(screen.getByLabelText("Database name")).toHaveValue("app_db");
    expect(screen.getByLabelText("S3 folder")).toHaveValue("import");
    // The stepper goes back to a done step, and forward again with Next.
    await userEvent.click(screen.getByRole("button", { name: "Go back to Verify" }));
    await waitFor(() =>
      expect(screen.getByRole("heading", { level: 2, name: "Step 2 of 6: Verify" })).toHaveFocus(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await screen.findByRole("heading", { level: 2, name: "Step 3 of 6: Destination" });
    expect(screen.getByText("s3://my-sqlserver-backups/import/app_db.bak")).toBeInTheDocument();

    // 4. Preview: one dry run of the job, the upload and the restore in it.
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(await screen.findByText("Restore app_db from import/app_db.bak")).toBeInTheDocument();
    expect(
      screen.getByText(/s3:\/\/my-sqlserver-backups\/import\/app_db\.bak · 1 parts/),
    ).toBeInTheDocument();
    expect(
      body(requestsTo(fetch, JOBS).map((init) => ({ url: new URL(ENGINE.url), init }))[0]),
    ).toEqual({
      kind: "import",
      target: "dev-sql",
      prefix: "import",
      items: [{ database: "app_db", file: PATH }],
      dryRun: true,
    });

    // 5. Progress: the upload of the job, then the restore and its task.
    await userEvent.click(screen.getByRole("button", { name: "Start import" }));
    await screen.findByRole("heading", { level: 2, name: "Step 5 of 6: Progress" });
    const bar = await screen.findByRole("progressbar", { name: "Upload of app_db" });
    expect(bar).toHaveAttribute("aria-valuenow", "50");
    expect(screen.getByText("512 B of 1 KiB · 1 MiB/s · 1:05 left")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Stop following" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel import" })).toBeInTheDocument();
    // While it runs, the done steps cannot be reopened.
    expect(
      within(screen.getByRole("navigation", { name: "Steps" })).queryAllByRole("button"),
    ).toHaveLength(0);
    stream.send("step.started", {});
    const task = await screen.findByRole("progressbar", { name: "RESTORE_DB app_db" });
    expect(task).toHaveAttribute("aria-valuenow", "40");
    stream.send("job.succeeded", {});
    stream.close();
    // Started with the digest and the key of the preview, and nothing to confirm.
    const [, start] = requestsTo(fetch, JOBS);
    expect(JSON.parse(String(start.body))).toEqual({
      kind: "import",
      target: "dev-sql",
      prefix: "import",
      items: [{ database: "app_db", file: PATH, key: "import/app_db.bak" }],
      digest: "digest-1",
    });

    // 6. Result: the post report with the orphaned users, and the job in Activity.
    await waitFor(() =>
      expect(screen.getByRole("heading", { level: 2, name: "Step 6 of 6: Result" })).toHaveFocus(),
    );
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
    expect(screen.getByRole("link", { name: "Follow it in Activity" })).toHaveAttribute(
      "href",
      `#activity/${JOB_ID}`,
    );
    expect(screen.getByRole("link", { name: "Open Databases" })).toHaveAttribute(
      "href",
      "#databases/dev-sql",
    );

    await userEvent.click(screen.getByRole("button", { name: "Import more files" }));
    await waitFor(() =>
      expect(screen.getByRole("heading", { level: 2, name: "Step 1 of 6: Files" })).toHaveFocus(),
    );
  });

  it("stops at a backup that cannot be restored and says what to do", async () => {
    const fetch = mockWithDatabases({
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
    await waitFor(() =>
      expect(screen.getByRole("heading", { level: 2, name: "Step 1 of 6: Files" })).toHaveFocus(),
    );
    expect(screen.queryByText("app_db.bak")).not.toBeInTheDocument();
    expect(requestsTo(fetch, INSPECT)).toHaveLength(1);
  });

  it("refuses a relative path and shows why the inspection failed", async () => {
    mockWithDatabases({
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

  it("says why the start was refused (412) and previews the job again", async () => {
    const reason = "the plan changed since the preview: preview it again";
    const fetch = mockWithDatabases({
      [TARGETS]: targets(DEV),
      [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
      [JOBS]: (request) => (body(request).dryRun ? json(PLAN) : json({ reason, plan: PLAN }, 412)),
    });
    open();
    await addAndVerify(PATH);
    await toPreview();
    await userEvent.click(await screen.findByRole("button", { name: "Start import" }));

    expect(await screen.findByText(reason)).toBeInTheDocument();
    // Still on the preview, which ran again.
    expect(screen.getByRole("heading", { level: 2, name: "Step 4 of 6: Preview" })).toBeVisible();
    await waitFor(() => expect(requestsTo(fetch, JOBS)).toHaveLength(3));
    expect(body({ url: new URL(ENGINE.url), init: requestsTo(fetch, JOBS)[2] }).dryRun).toBe(true);
    expect(await screen.findByRole("button", { name: "Start import" })).toBeEnabled();
  });

  it("shows the message of a confirmation the engine refuses (422) and stays on the preview", async () => {
    mockWithDatabases({
      [TARGETS]: targets(DEV),
      [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
      [JOBS]: (request) =>
        body(request).dryRun
          ? json(PLAN)
          : text('not confirmed: this import asks to retype "dev-sql" in confirm\n', 422),
    });
    open();
    await addAndVerify(PATH);
    await toPreview();
    await userEvent.click(await screen.findByRole("button", { name: "Start import" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("not confirmed: this import asks");
    expect(screen.getByRole("heading", { level: 2, name: "Step 4 of 6: Preview" })).toBeVisible();
  });

  it("says the engine has no job API and does not start", async () => {
    mockWithDatabases({
      [TARGETS]: targets(DEV),
      [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
      [JOBS]: () => text("Method Not Allowed\n", 405),
    });
    open();
    await addAndVerify(PATH);
    await toPreview();
    expect(
      await screen.findByText(
        "This engine cannot run imports as jobs yet: update the Dialga engine.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start import" })).toBeDisabled();
  });

  it("asks to retype the name of a protected instance before it starts", async () => {
    mockWithDatabases({
      [TARGETS]: targets(DEV, PROD),
      [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
      [JOBS]: () =>
        json({ ...PLAN, target: "prod-sql", protected: true, confirmation: "prod-sql" }),
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
    mockWithDatabases({
      [TARGETS]: targets(DEV),
      [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
      [JOBS]: () => json({ ...PLAN, confirmation: "import/app_db.bak" }),
    });
    open();
    await addAndVerify(PATH);
    await toPreview();
    const confirm = await screen.findByLabelText("Type import/app_db.bak to confirm");
    expect(confirm).toHaveAccessibleDescription(
      "The upload replaces objects already in the bucket: retype what it replaces to confirm.",
    );
  });

  it("offers to replace a file the plan refuses, and sends overwrite with it", async () => {
    const refused = {
      ...PLAN,
      ready: false,
      items: [
        {
          ...PLAN.items[0],
          ready: false,
          rejected: [{ path: PATH, reason: "s3://my-sqlserver-backups/import/app_db.bak exists" }],
        },
      ],
    };
    const fetch = mockWithDatabases({
      [TARGETS]: targets(DEV),
      [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
      [JOBS]: (request) =>
        (body(request).items as { overwrite?: boolean }[])[0].overwrite
          ? json(PLAN)
          : json(refused),
    });
    open();
    await addAndVerify(PATH);
    await toPreview();
    expect(await screen.findByText(/app_db\.bak: s3:\/\/.* exists/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start import" })).toBeDisabled();
    await userEvent.click(screen.getByLabelText("Replace the objects that already exist"));
    await waitFor(() => expect(screen.getByRole("button", { name: "Start import" })).toBeEnabled());
    const sent = requestsTo(fetch, JOBS).map((init) => JSON.parse(String(init.body)));
    expect(sent.at(-1).items).toEqual([{ database: "app_db", file: PATH, overwrite: true }]);
  });

  it("goes on in the engine when the page stops following the job", async () => {
    const stream = sseStream();
    mockWithDatabases({
      [TARGETS]: targets(DEV),
      [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
      [JOBS]: jobs(),
      [EVENTS]: () => stream.response,
      [JOB]: () => json(snapshot("running", "upload")),
    });
    open();
    await addAndVerify(PATH);
    await toPreview();
    await userEvent.click(await screen.findByRole("button", { name: "Start import" }));
    await userEvent.click(await screen.findByRole("button", { name: "Stop following" }));
    // A real fetch fails its body read on abort; the fake one needs the stream to end.
    stream.close();

    expect(
      await screen.findByRole("heading", { level: 2, name: "Step 6 of 6: Result" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "Stopped following: the import goes on in the engine. Follow it in Activity.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Follow it in Activity" })).toHaveAttribute(
      "href",
      `#activity/${JOB_ID}`,
    );
  });

  it("shows the database that failed with its error, and the others", async () => {
    const stream = sseStream();
    mockWithDatabases({
      [TARGETS]: targets(DEV),
      [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
      [JOBS]: jobs(),
      [EVENTS]: () => stream.response,
      [JOB]: () => json(snapshot("failed", "restore")),
    });
    open();
    await addAndVerify(PATH);
    await toPreview();
    await userEvent.click(await screen.findByRole("button", { name: "Start import" }));
    await screen.findByRole("heading", { level: 2, name: "Step 5 of 6: Progress" });
    stream.send("job.failed", {});
    stream.close();

    const result = await screen.findByRole("region", { name: "Database app_db" });
    expect(within(result).getByText("RDS task 7 ended in ERROR")).toBeInTheDocument();
    expect(screen.getByText("Restored: 0 · failed: 1 · not completed: 0")).toBeInTheDocument();
  });

  describe("over an existing database", () => {
    const OLD = "app_db_old_20261016T100000";
    const REPLACE_PLAN: JobPlan = {
      ...PLAN,
      confirmation: "app_db",
      items: [
        {
          ...PLAN.items[0],
          checks: [
            { step: "database", label: "Target database", status: "ok", detail: "app_db exists" },
            {
              step: "replace",
              label: "Replace",
              status: "ok",
              detail: "swap: two copies (2 × 8.0 GiB) fit in 100 GiB free",
            },
          ],
        },
      ],
    };

    async function toDestination() {
      open();
      await addAndVerify(PATH);
      await userEvent.click(await screen.findByRole("button", { name: "Next" }));
      await screen.findByRole("heading", { level: 2, name: "Step 3 of 6: Destination" });
    }

    it("offers nothing when the name is new", async () => {
      mockWithDatabases({
        [TARGETS]: targets(DEV),
        [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
      });
      await toDestination();
      expect(screen.queryByRole("radiogroup")).toBeNull();
    });

    it("stops by default and sends the dry run without replace", async () => {
      const fetch = mockWithDatabases(
        {
          [TARGETS]: targets(DEV),
          [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
          [JOBS]: () => json(PLAN),
        },
        ["APP_DB"],
      );
      await toDestination();
      const group = await screen.findByRole("radiogroup", {
        name: "The database app_db already exists",
      });
      expect(within(group).getByRole("radio", { name: "Stop" })).toBeChecked();
      await userEvent.click(screen.getByRole("button", { name: "Preview" }));
      await screen.findByRole("heading", { level: 2, name: "Step 4 of 6: Preview" });
      expect(body({ url: new URL(ENGINE.url), init: requestsTo(fetch, JOBS)[0] }).items).toEqual([
        { database: "app_db", file: PATH },
      ]);
    });

    it("hides the choice again when the name changes to a new one", async () => {
      mockWithDatabases(
        {
          [TARGETS]: targets(DEV),
          [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
        },
        ["app_db"],
      );
      await toDestination();
      await userEvent.click(await screen.findByRole("radio", { name: "Keep the old one (swap)" }));
      await userEvent.type(screen.getByLabelText("Database name"), "_2");
      expect(screen.queryByRole("radiogroup")).toBeNull();
    });

    it("swaps: sends replace, asks for the name, names the old database and offers to drop it", async () => {
      const stream = sseStream();
      const base = snapshot("succeeded", "restore");
      const started = {
        ...base,
        items: [{ ...base.items[0], result: `kept the old database as ${OLD}` }],
      };
      const fetch = mockWithDatabases(
        {
          [TARGETS]: targets(DEV),
          [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
          [JOBS]: jobs(REPLACE_PLAN),
          [EVENTS]: () => stream.response,
          [JOB]: () => json(started),
          [REPORT]: () => json(REPORT_OK),
          "POST /api/v1/targets/dev-sql/plans/drop": () =>
            json({
              plan: {
                operation: "drop",
                target: "dev-sql",
                instance: "my-sqlserver",
                protected: false,
                actions: [],
              },
              checks: [],
              ready: true,
              confirmation: OLD,
              digest: "d",
              preview: "",
              request: { database: OLD },
            }),
        },
        ["app_db"],
      );
      await toDestination();
      await userEvent.click(await screen.findByRole("radio", { name: "Keep the old one (swap)" }));
      await userEvent.click(screen.getByRole("button", { name: "Preview" }));
      await screen.findByRole("heading", { level: 2, name: "Step 4 of 6: Preview" });
      expect(body({ url: new URL(ENGINE.url), init: requestsTo(fetch, JOBS)[0] }).items).toEqual([
        { database: "app_db", file: PATH, replace: "swap" },
      ]);
      expect(await screen.findByText(/swap: two copies/)).toBeInTheDocument();

      const start = screen.getByRole("button", { name: "Start import" });
      expect(start).toBeDisabled();
      const confirm = screen.getByLabelText("Type app_db to confirm");
      expect(confirm).toHaveAccessibleDescription("Replacing a database asks to retype its name.");
      await userEvent.type(confirm, "app_db");
      await userEvent.click(start);
      await screen.findByRole("heading", { level: 2, name: "Step 5 of 6: Progress" });
      expect(JSON.parse(String(requestsTo(fetch, JOBS)[1].body))).toMatchObject({
        items: [{ database: "app_db", replace: "swap" }],
        confirm: "app_db",
      });
      stream.send("job.succeeded", {});
      stream.close();

      const outcome = await screen.findByRole("region", { name: "Result of the replace" });
      expect(
        within(outcome).getByText(`The previous database was kept as ${OLD}.`),
      ).toBeInTheDocument();
      await userEvent.click(within(outcome).getByRole("button", { name: `Drop ${OLD}` }));
      expect(await screen.findByRole("dialog", { name: `Drop ${OLD}` })).toBeInTheDocument();
    });

    it("sends drop with the safety backup unless it is switched off", async () => {
      const fetch = mockWithDatabases(
        {
          [TARGETS]: targets(DEV),
          [INSPECT]: () => events(["result", RESTORABLE], ["done", { ok: true }]),
          [JOBS]: () => json(PLAN),
        },
        ["app_db"],
      );
      await toDestination();
      await userEvent.click(await screen.findByRole("radio", { name: "Drop it and restore" }));
      await userEvent.click(screen.getByRole("button", { name: "Preview" }));
      await screen.findByRole("heading", { level: 2, name: "Step 4 of 6: Preview" });
      expect(body({ url: new URL(ENGINE.url), init: requestsTo(fetch, JOBS)[0] }).items).toEqual([
        { database: "app_db", file: PATH, replace: "drop", backupFirst: true },
      ]);

      await userEvent.click(screen.getByRole("button", { name: "Back" }));
      await userEvent.click(
        await screen.findByRole("checkbox", { name: "Back up the current database to S3 first" }),
      );
      expect(screen.getByRole("alert")).toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "Preview" }));
      await screen.findByRole("heading", { level: 2, name: "Step 4 of 6: Preview" });
      await waitFor(() => expect(requestsTo(fetch, JOBS)).toHaveLength(2));
      expect(body({ url: new URL(ENGINE.url), init: requestsTo(fetch, JOBS)[1] }).items).toEqual([
        { database: "app_db", file: PATH, replace: "drop", backupFirst: false },
      ]);
    });
  });

  it("speaks Italian", async () => {
    mockWithDatabases({ [TARGETS]: targets(DEV) });
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
