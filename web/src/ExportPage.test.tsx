import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Database, Overview, Task } from "./api";
import { ExportPage } from "./ExportPage";
import { resetBackends } from "./exportBackend";
import type { PlanPreview } from "./plans";
import { CONFIG_PATH, DEV, PROD, renderI18n } from "./test/fixtures";
import { ENGINE, json, mockFetch, type Route, requestsTo, sseStream, text } from "./test/http";

const db = (name: string, more: Partial<Database> = {}): Database => ({
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
  ...more,
});
const SALES = db("sales", { dataMB: 6000 });
const CRM = db("crm");
const STAGED = db("staged", { state: "RESTORING" });

const OVERVIEW: Overview = {
  target: "dev-sql",
  instance: "my-sqlserver",
  status: "available",
  engine: "sqlserver-se",
  engineVersion: "14.00.3465.1.v1",
  class: "db.m6i.large",
  allocatedGiB: 200,
  sql: "ok",
  userDatabases: 3,
  activeTasks: 0,
  backupWindow: "04:29-04:59",
  maintenanceWindow: "sun:05:00-sun:05:30",
};

function preview(database: string, target = "dev-sql", confirmation?: string): PlanPreview {
  const key = `export/${database}_20261016-042000.bak`;
  return {
    plan: {
      operation: "export",
      target,
      instance: "my-sqlserver",
      protected: !!confirmation,
      subject: database,
      actions: [
        {
          id: "export",
          title: `Back up ${database} to s3://my-sqlserver-backups/${key}`,
          kind: "sql",
          risk: "change",
          statement: `exec msdb.dbo.rds_backup_database @source_db_name = N'${database}';`,
        },
      ],
    },
    checks: [
      { step: "database", label: "Database", status: "ok", detail: `${database} is ONLINE` },
      {
        step: "destination",
        label: "Destination in S3",
        status: "ok",
        detail: `s3://my-sqlserver-backups/${key} is free`,
      },
    ],
    ready: true,
    confirmation,
    digest: "d".repeat(32),
    preview: "Dry run",
    request: { database, key },
  };
}

const task = (lifecycle: Task["lifecycle"], percent: number): Task => ({
  id: 21,
  type: "BACKUP_DB",
  database: "sales",
  percent,
  lifecycle,
  s3Object: "arn:aws:s3:::my-sqlserver-backups/export/sales_20261016-042000.bak",
});

/** An engine without the job route: the mux answers 405 to POST /api/v1/jobs. */
const NO_JOBS = { "POST /api/v1/jobs": () => text("Method Not Allowed\n", 405) };

function serve(routes: Record<string, Route> = {}, targets = [DEV]) {
  return mockFetch({
    ...NO_JOBS,
    "GET /api/v1/targets": () => json({ configPath: CONFIG_PATH, targets }),
    "GET /api/v1/targets/dev-sql/databases": () => json({ databases: [SALES, CRM, STAGED] }),
    "GET /api/v1/targets/dev-sql/overview": () => json(OVERVIEW),
    "POST /api/v1/targets/dev-sql/plans/export": ({ init }) =>
      json(preview(JSON.parse(String(init.body)).database)),
    ...routes,
  });
}

const panel = (name: RegExp) => within(screen.getByRole("region", { name }));
const next = () => userEvent.click(screen.getByRole("button", { name: "Next" }));

beforeEach(() => {
  resetBackends();
});

describe("ExportPage", () => {
  it("exports the databases of the address, with a download, step by step", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    // Friday 04:20 UTC: 6 GB of backups (about 12 min) cross the 04:29 backup window.
    vi.setSystemTime(new Date("2026-10-16T04:20:00Z"));
    window.location.hash = "#export/dev-sql?db=sales";
    const execute = sseStream();
    const fetch = serve({
      "POST /api/v1/targets/dev-sql/plans/export/execute": () => execute.response,
      "POST /api/v1/targets/dev-sql/downloads": () =>
        new Response(
          [
            `event: plan\ndata: ${JSON.stringify({ confirmation: "", items: [], rejected: [] })}\n\n`,
            `event: done\ndata: ${JSON.stringify({
              ok: true,
              results: [
                {
                  path: "/home/u/backups/sales_20261016-042000.bak",
                  bucket: "my-sqlserver-backups",
                  key: "export/sales_20261016-042000.bak",
                  size: 6 * 1024 ** 3,
                  checksumAlgorithm: "SHA256",
                  checksum: "q1w2e3r4t5y6u7i8o9p0a1s2d3f4g5h6==",
                  verified: true,
                  seconds: 60,
                  bytesPerSecond: 1e8,
                },
              ],
            })}\n\n`,
          ].join(""),
          { headers: { "Content-Type": "text/event-stream" } },
        ),
    });
    renderI18n(<ExportPage engine={ENGINE} engineError="" />);

    // 1. Databases: the online ones, sales ticked from the address.
    const step1 = panel(/^Step 1 of 6: Databases/);
    expect(await step1.findByRole("checkbox", { name: /sales/ })).toBeChecked();
    expect(step1.getByRole("checkbox", { name: /crm/ })).not.toBeChecked();
    expect(step1.queryByRole("checkbox", { name: /staged/ })).toBeNull();
    expect(step1.getByText("Not online, not listed: staged")).toBeInTheDocument();
    await next();

    // 2. Options: a bad key and a relative folder are refused, with the focus on the first.
    const heading2 = screen.getByRole("heading", { name: "Step 2 of 6: Options" });
    expect(heading2).toHaveFocus();
    const key = screen.getByRole("textbox", { name: "S3 key of sales" });
    await userEvent.type(key, "export/a b.bak");
    await userEvent.click(screen.getByRole("checkbox", { name: /Download each backup/ }));
    await userEvent.type(screen.getByRole("textbox", { name: "Folder" }), "backups");
    await next();
    expect(screen.getByRole("alert")).toHaveTextContent("Fix the fields marked as invalid.");
    expect(key).toHaveAttribute("aria-invalid", "true");
    expect(key).toHaveFocus();
    expect(screen.getByText(/Use an absolute path/)).toBeInTheDocument();
    await userEvent.clear(key);
    const folder = screen.getByRole("textbox", { name: "Folder" });
    await userEvent.clear(folder);
    await userEvent.type(folder, "/home/u/backups");
    await next();

    // 3. Windows: the backups would cross the automated backup window.
    const step3 = panel(/^Step 3 of 6: Windows/);
    expect(await step3.findByText(/would cross the automated backup window/)).toBeInTheDocument();
    expect(step3.getByText("sun:05:00-sun:05:30")).toBeInTheDocument();
    await next();

    // 4. Preview: one plan per database, the S3 object and the local file.
    const step4 = panel(/^Step 4 of 6: Preview/);
    expect(
      await step4.findByText("s3://my-sqlserver-backups/export/sales_20261016-042000.bak"),
    ).toBeInTheDocument();
    expect(step4.getByText("/home/u/backups/sales_20261016-042000.bak")).toBeInTheDocument();
    // The empty key asks the engine for its default.
    expect(
      JSON.parse(String(requestsTo(fetch, "POST /api/v1/targets/dev-sql/plans/export")[0]?.body)),
    ).toEqual({
      database: "sales",
    });
    await userEvent.click(screen.getByRole("button", { name: "Run export" }));

    // 5. Progress: the task, then the result.
    await screen.findByRole("heading", { name: "Step 5 of 6: Progress" });
    expect(screen.getByRole("navigation", { name: "Steps" })).not.toContainElement(
      screen.queryByRole("button", { name: /^Go back to/ }),
    );
    execute.send("step", {
      step: "export",
      label: "Back up",
      status: "ok",
      detail: "task 21 created",
    });
    execute.send("task", task("IN_PROGRESS", 40));
    expect(await screen.findByRole("progressbar", { name: "BACKUP_DB sales" })).toHaveAttribute(
      "aria-valuenow",
      "40",
    );
    expect(screen.getByRole("status", { name: "" })).toHaveTextContent("Backing up sales…");
    execute.send("task", task("SUCCESS", 100));
    execute.send("done", { ok: true, task: task("SUCCESS", 100) });
    execute.close();

    // 6. Result: where the backup went and its checksum.
    const step6 = within(await screen.findByRole("region", { name: "Step 6 of 6: Result" }));
    expect(step6.getByText("Exported: 1 · failed: 0 · not completed: 0")).toBeInTheDocument();
    expect(
      step6.getByText("s3://my-sqlserver-backups/export/sales_20261016-042000.bak"),
    ).toBeInTheDocument();
    expect(step6.getByText("/home/u/backups/sales_20261016-042000.bak")).toBeInTheDocument();
    expect(step6.getByText("Verified")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Step 6 of 6: Result" })).toHaveFocus();
    await waitFor(() =>
      expect(screen.getByText(/^Export finished\. Exported: 1/)).toBeInTheDocument(),
    );
  });

  it("asks for at least one database", async () => {
    serve();
    renderI18n(<ExportPage engine={ENGINE} engineError="" />);
    await screen.findByRole("checkbox", { name: /sales/ });
    await next();
    expect(screen.getByRole("alert")).toHaveTextContent("Choose at least one database.");
    // The first database of the list, by name.
    expect(screen.getByRole("checkbox", { name: /crm/ })).toHaveFocus();
    expect(screen.getByRole("heading", { name: "Step 1 of 6: Databases" })).toBeInTheDocument();
  });

  it("runs on a protected target only after its name is retyped", async () => {
    window.location.hash = "#export/prod-sql?db=crm";
    mockFetch({
      ...NO_JOBS,
      "GET /api/v1/targets": () => json({ configPath: CONFIG_PATH, targets: [DEV, PROD] }),
      "GET /api/v1/targets/prod-sql/databases": () => json({ databases: [CRM] }),
      "GET /api/v1/targets/prod-sql/overview": () => json({ ...OVERVIEW, target: "prod-sql" }),
      "POST /api/v1/targets/prod-sql/plans/export": () =>
        json(preview("crm", "prod-sql", "prod-sql")),
    });
    renderI18n(<ExportPage engine={ENGINE} engineError="" />);
    expect(await screen.findByRole("checkbox", { name: /crm/ })).toBeChecked();
    await next();
    await next();
    await screen.findByRole("heading", { name: "Step 3 of 6: Windows" });
    await next();
    const run = await screen.findByRole("button", { name: "Run export" });
    expect(screen.getByText("Protected target")).toBeInTheDocument();
    expect(run).toBeDisabled();
    const confirm = screen.getByRole("textbox", { name: "Type prod-sql to confirm" });
    await userEvent.type(confirm, "prod");
    expect(run).toBeDisabled();
    await userEvent.type(confirm, "-sql");
    expect(run).toBeEnabled();
  });

  it("tells why a plan is not ready and keeps Run export off", async () => {
    window.location.hash = "#export/dev-sql?db=crm";
    serve({
      "POST /api/v1/targets/dev-sql/plans/export": () =>
        json({
          ...preview("crm"),
          ready: false,
          checks: [
            {
              step: "destination",
              label: "Destination in S3",
              status: "failed",
              detail:
                "s3://my-sqlserver-backups/export/crm.bak already exists: an export never overwrites, choose another key",
            },
          ],
        }),
    });
    renderI18n(<ExportPage engine={ENGINE} engineError="" />);
    await screen.findByRole("checkbox", { name: /crm/ });
    await next();
    await next();
    await screen.findByRole("heading", { name: "Step 3 of 6: Windows" });
    await next();
    expect(await screen.findByText(/Some checks failed/)).toBeInTheDocument();
    expect(screen.getByText("Not ready")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Run export" })).toBeDisabled();
    // Back to the options from the stepper.
    await userEvent.click(screen.getByRole("button", { name: "Go back to Options" }));
    expect(screen.getByRole("heading", { name: "Step 2 of 6: Options" })).toHaveFocus();
  });

  it("runs as one engine job when the engine has the route", async () => {
    window.location.hash = "#export/dev-sql?db=crm";
    const KEY = "export/crm_20261016-042000.bak";
    const plan = {
      kind: "export",
      target: "dev-sql",
      instance: "my-sqlserver",
      protected: false,
      ready: true,
      digest: "j".repeat(32),
      items: [
        {
          database: "crm",
          file: "",
          key: KEY,
          ready: true,
          checks: [
            {
              step: "destination",
              label: "Destination in S3",
              status: "ok",
              detail: `s3://my-sqlserver-backups/${KEY} is free`,
            },
          ],
          actions: [
            {
              id: "backup",
              title: "Back up crm",
              statement: "exec msdb.dbo.rds_backup_database …",
            },
          ],
        },
      ],
      preview: "Dry run",
    };
    const snapshot = (state: string, lifecycle: Task["lifecycle"], percent: number) => ({
      id: "20261016T042000Z-0a1b2c3d",
      kind: "export",
      target: "dev-sql",
      state,
      items: [
        {
          name: "crm",
          state,
          step: "wait",
          percent,
          steps: [
            { id: "backup", title: "Back up crm", status: "succeeded" },
            {
              id: "wait",
              title: "Wait for the backup task",
              status: state === "running" ? "running" : "succeeded",
            },
          ],
          task: { id: 22, type: "BACKUP_DB", lifecycle, percent },
        },
      ],
    });
    const events = sseStream();
    const fetch = serve({
      "POST /api/v1/jobs": (_, call) =>
        call === 0 ? json(plan) : json(snapshot("running", "IN_PROGRESS", 10), 201),
      "GET /api/v1/jobs/20261016T042000Z-0a1b2c3d/events": () => events.response,
      "GET /api/v1/jobs/20261016T042000Z-0a1b2c3d": (_, call) =>
        call === 0
          ? json(snapshot("running", "IN_PROGRESS", 50))
          : json(snapshot("succeeded", "SUCCESS", 100)),
    });
    renderI18n(<ExportPage engine={ENGINE} engineError="" />);
    await screen.findByRole("checkbox", { name: /crm/ });
    await next();
    await next();
    await screen.findByRole("heading", { name: "Step 3 of 6: Windows" });
    await next();
    expect(await screen.findByText(/runs the export as one job/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Run export" }));

    expect(await screen.findByRole("button", { name: "Cancel export" })).toBeInTheDocument();
    events.send("step.started", {});
    expect(await screen.findByRole("progressbar", { name: "BACKUP_DB crm" })).toHaveAttribute(
      "aria-valuenow",
      "50",
    );
    events.send("job.succeeded", {});
    events.close();
    const result = within(await screen.findByRole("region", { name: "Step 6 of 6: Result" }));
    expect(result.getByText("Exported: 1 · failed: 0 · not completed: 0")).toBeInTheDocument();
    expect(result.getByText(`s3://my-sqlserver-backups/${KEY}`)).toBeInTheDocument();
    // Started with the key the dry run resolved, and its digest.
    expect(JSON.parse(String(requestsTo(fetch, "POST /api/v1/jobs")[1]?.body))).toMatchObject({
      items: [{ database: "crm", key: KEY }],
      digest: "j".repeat(32),
    });
  });
});
