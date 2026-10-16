import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { PlanDialog, type PlanResult } from "./PlanDialog";
import type { Operation, PlanRequest } from "./plans";
import { renderI18n } from "./test/fixtures";
import { deferred, ENGINE, json, mockFetch, requestsTo, sseStream, text } from "./test/http";
import {
  CANCEL_PREVIEW,
  DROP_PREVIEW,
  EXPORT_PREVIEW,
  FINISH_NOT_READY,
  PROTECTED_DROP,
  RESTORE_PREVIEW,
} from "./test/plans";

const plans = (op: Operation, target = "dev-sql") => `POST /api/v1/targets/${target}/plans/${op}`;
const execute = (op: Operation, target = "dev-sql") => `${plans(op, target)}/execute`;

function open(
  operation: Operation,
  request: PlanRequest,
  extra: { fields?: "export" | "restore"; target?: string; lang?: "en" | "it" } = {},
) {
  const onClose = vi.fn<(result: PlanResult) => void>();
  const onShowReport = vi.fn<(database: string) => void>();
  renderI18n(
    <PlanDialog
      engine={ENGINE}
      target={extra.target ?? "dev-sql"}
      operation={operation}
      request={request}
      fields={extra.fields}
      onClose={onClose}
      onShowReport={onShowReport}
    />,
    extra.lang,
  );
  return { onClose, onShowReport, dialog: () => screen.getByRole("dialog") };
}

const body = (init: RequestInit) => JSON.parse(String(init.body));

describe("PlanDialog", () => {
  it("previews a drop and runs it only on the exact database name", async () => {
    const fetch = mockFetch({ [plans("drop")]: () => json(DROP_PREVIEW) });
    const { dialog } = open("drop", { database: "sales" });
    expect(dialog()).toHaveAccessibleName("Drop sales");
    expect(screen.getByText("Running the pre-flight checks…")).toBeInTheDocument();

    const confirm = await screen.findByRole("textbox", { name: "Type sales to confirm" });
    expect(body(requestsTo(fetch, plans("drop"))[0])).toEqual({ database: "sales" });
    // The checks, with the labels of the UI and the details of the engine.
    const checks = within(screen.getByRole("region", { name: "Pre-flight checks" }));
    expect(checks.getByText("Database")).toBeInTheDocument();
    expect(checks.getByText("sales is ONLINE")).toBeInTheDocument();
    expect(checks.getByText("Native tasks")).toBeInTheDocument();
    // The action, destructive, with its exact T-SQL behind Details.
    const actions = within(screen.getByRole("region", { name: "What runs" }));
    expect(actions.getByText("Drop database sales")).toBeInTheDocument();
    expect(actions.getByText("Destructive")).toHaveClass("badge-failed");
    expect(actions.getByText("Details")).toBeInTheDocument();
    expect(dialog().querySelector("pre.statement")?.textContent).toBe(
      "exec msdb.dbo.rds_drop_database @db_name = N'sales';",
    );
    expect(
      screen.getByText("This drops sales on dev-sql for good: it cannot be undone."),
    ).toBeInTheDocument();
    // Focused by an effect after the preview renders.
    await waitFor(() => expect(confirm).toHaveFocus());
    expect(confirm).toHaveAccessibleDescription(
      "This drops sales on dev-sql for good: it cannot be undone.",
    );

    const run = screen.getByRole("button", { name: "Drop database" });
    expect(run).toHaveClass("btn-destructive");
    expect(run).toBeDisabled();
    await userEvent.type(confirm, "Sales");
    expect(run).toBeDisabled();
    await userEvent.clear(confirm);
    await userEvent.type(confirm, "sales ");
    expect(run).toBeDisabled();
    await userEvent.clear(confirm);
    await userEvent.type(confirm, "sales");
    expect(run).toBeEnabled();
    expect(requestsTo(fetch, execute("drop"))).toHaveLength(0);
  });

  it("asks for the target name on a protected target", async () => {
    mockFetch({ [plans("drop", "prod-sql")]: () => json(PROTECTED_DROP) });
    open("drop", { database: "sales" }, { target: "prod-sql" });
    const confirm = await screen.findByRole("textbox", { name: "Type prod-sql to confirm" });
    expect(screen.getByText("Protected target")).toBeInTheDocument();
    expect(
      screen.getByText(/prod-sql is protected: every change asks to retype its name\./),
    ).toBeInTheDocument();
    await userEvent.type(confirm, "sales");
    expect(screen.getByRole("button", { name: "Drop database" })).toBeDisabled();
    await userEvent.clear(confirm);
    await userEvent.type(confirm, "prod-sql");
    expect(screen.getByRole("button", { name: "Drop database" })).toBeEnabled();
  });

  it("streams the execution with the progress of the task", async () => {
    const stream = sseStream();
    const fetch = mockFetch({
      [plans("restore")]: () => json(RESTORE_PREVIEW),
      [execute("restore")]: () => stream.response,
    });
    const { onShowReport } = open(
      "restore",
      { database: "sales_copy", key: "sales.bak" },
      { fields: "restore" },
    );
    await userEvent.click(await screen.findByRole("button", { name: "Restore" }));
    expect(body(requestsTo(fetch, execute("restore"))[0])).toEqual({
      database: "sales_copy",
      key: "import/sales.bak",
      digest: "r".repeat(32),
    });

    stream.send("notice", { message: "Native tasks: 1 native task(s) running or queued" });
    stream.send("step", { step: "restore", label: "Restore", status: "running" });
    stream.send("step", {
      step: "restore",
      label: "Restore",
      status: "ok",
      detail: "task 12 created",
    });
    stream.send("step", { step: "wait", label: "Wait for task 12", status: "running" });
    const task = {
      id: 12,
      type: "RESTORE_DB",
      database: "sales_copy",
      percent: 0,
      lifecycle: "CREATED",
    };
    stream.send("task", task);
    const live = await screen.findByText("Task 12: Queued");
    expect(live).toHaveAttribute("aria-live", "polite");
    stream.send("task", { ...task, lifecycle: "IN_PROGRESS", percent: 40 });
    const bar = await screen.findByRole("progressbar", { name: "RESTORE_DB sales_copy" });
    await waitFor(() => expect(bar).toHaveAttribute("aria-valuenow", "40"));
    expect(live).toHaveTextContent("Task 12: In progress");
    stream.send("task", { ...task, lifecycle: "IN_PROGRESS", percent: 80 });
    await waitFor(() => expect(bar).toHaveAttribute("aria-valuenow", "80"));
    // Percentages are not announced, only lifecycle changes.
    expect(live).toHaveTextContent("Task 12: In progress");
    expect(screen.getByText(/Closing this window stops following the task/)).toHaveTextContent(
      "dialga wait 12 -t dev-sql",
    );
    expect(
      screen.getByText("Native tasks: 1 native task(s) running or queued"),
    ).toBeInTheDocument();
    expect(screen.getByText("Restore sales_copy from import/sales.bak")).toBeInTheDocument();

    stream.send("task", { ...task, lifecycle: "SUCCESS", percent: 100, info: "[x] Done." });
    stream.send("step", { step: "wait", label: "Wait for task 12", status: "ok" });
    stream.send("done", { ok: true, task: { ...task, lifecycle: "SUCCESS", percent: 100 } });
    stream.close();
    expect(
      await screen.findByText(
        "Restore completed. Check the database with the report: owner, orphaned users, objects.",
        { selector: ".banner-text" },
      ),
    ).toBeInTheDocument();
    expect(live).toHaveTextContent("Restore completed.");
    expect(screen.queryByRole("progressbar")).toBeNull();
    expect(screen.getByText("Succeeded")).toHaveClass("badge-ok");
    await userEvent.click(screen.getByRole("button", { name: "Open the report" }));
    expect(onShowReport).toHaveBeenCalledWith("sales_copy");
  });

  it("asks for the restore values first and a new preview after an edit", async () => {
    const fetch = mockFetch({ [plans("restore")]: () => json(RESTORE_PREVIEW) });
    open("restore", {}, { fields: "restore" });
    const preview = screen.getByRole("button", { name: "Preview" });
    expect(preview).toBeDisabled();
    expect(requestsTo(fetch, plans("restore"))).toHaveLength(0);

    await userEvent.type(screen.getByRole("textbox", { name: "Database name" }), "sales_copy");
    const key = screen.getByRole("textbox", { name: "S3 key of the backup" });
    await userEvent.type(key, "sales.bak{Enter}");
    await screen.findByRole("button", { name: "Restore" });
    expect(body(requestsTo(fetch, plans("restore"))[0])).toEqual({
      database: "sales_copy",
      key: "sales.bak",
    });
    expect(screen.getByText("Key in the bucket: import/sales.bak")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restore" })).toBeEnabled();

    await userEvent.type(key, "x");
    expect(
      screen.getByText("The values changed: preview again before running."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restore" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Preview again" }));
    await waitFor(() => expect(requestsTo(fetch, plans("restore"))).toHaveLength(2));
    expect(body(requestsTo(fetch, plans("restore"))[1]).key).toBe("sales.bakx");
    await waitFor(() => expect(screen.getByRole("button", { name: "Restore" })).toBeEnabled());
  });

  it("shows the default export key and sends the previewed one", async () => {
    const stream = sseStream();
    const fetch = mockFetch({
      [plans("export")]: () => json(EXPORT_PREVIEW),
      [execute("export")]: () => stream.response,
    });
    open("export", { database: "sales" }, { fields: "export" });
    const key = screen.getByRole("textbox", { name: "S3 key of the backup" });
    await waitFor(() => expect(key).toHaveValue("export/sales_20261016-083000.bak"));
    expect(body(requestsTo(fetch, plans("export"))[0])).toEqual({ database: "sales" });
    expect(screen.getByText("Destination in S3")).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: /to confirm/ })).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Export" }));
    expect(body(requestsTo(fetch, execute("export"))[0]).key).toBe(
      "export/sales_20261016-083000.bak",
    );
    stream.send("done", {
      ok: true,
      task: {
        id: 3,
        type: "BACKUP_DB",
        database: "sales",
        percent: 100,
        lifecycle: "SUCCESS",
        s3Object: "arn:aws:s3:::my-sqlserver-backups/export/sales_20261016-083000.bak",
      },
    });
    stream.close();
    expect(
      await screen.findByText(
        "Backup completed: s3://my-sqlserver-backups/export/sales_20261016-083000.bak",
        { selector: ".banner-text" },
      ),
    ).toBeInTheDocument();
  });

  it("previews again when the plan changed since the preview (412)", async () => {
    const fresh = { ...DROP_PREVIEW, digest: "n".repeat(32) };
    const fetch = mockFetch({
      [plans("drop")]: (_r, call) => json(call === 0 ? DROP_PREVIEW : fresh),
      [execute("drop")]: () => text("the plan changed since the preview: preview it again\n", 412),
    });
    open("drop", { database: "sales" });
    await userEvent.type(
      await screen.findByRole("textbox", { name: "Type sales to confirm" }),
      "sales",
    );
    await userEvent.click(screen.getByRole("button", { name: "Drop database" }));

    expect(
      await screen.findByText(
        "The situation changed since the preview: review the new preview before running.",
      ),
    ).toBeInTheDocument();
    await waitFor(() => expect(requestsTo(fetch, plans("drop"))).toHaveLength(2));
    // A new preview means a new confirmation.
    const confirm = await screen.findByRole("textbox", { name: "Type sales to confirm" });
    expect(confirm).toHaveValue("");
    await userEvent.type(confirm, "sales");
    await userEvent.click(screen.getByRole("button", { name: "Drop database" }));
    await waitFor(() => expect(requestsTo(fetch, execute("drop"))).toHaveLength(2));
    expect(body(requestsTo(fetch, execute("drop"))[1]).digest).toBe("n".repeat(32));
  });

  it("stays on the preview when the engine refuses the execution (422)", async () => {
    mockFetch({
      [plans("drop")]: () => json(DROP_PREVIEW),
      [execute("drop")]: () => text("SQL password: not set\n", 422),
    });
    open("drop", { database: "sales" });
    await userEvent.type(
      await screen.findByRole("textbox", { name: "Type sales to confirm" }),
      "sales",
    );
    await userEvent.click(screen.getByRole("button", { name: "Drop database" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("SQL password: not set");
    expect(alert).toHaveTextContent("The engine needs the SQL password in its environment");
    expect(screen.getByRole("button", { name: "Drop database" })).toBeInTheDocument();
  });

  it("explains a preview that the engine cannot make", async () => {
    mockFetch({ [plans("drop")]: () => text("RDS instance my-sqlserver: not found\n", 502) });
    open("drop", { database: "sales" });
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("RDS instance my-sqlserver: not found");
    expect(alert).toHaveTextContent("AWS or SQL Server did not answer");
    expect(screen.getByRole("button", { name: "Drop database" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Preview again" })).toBeEnabled();
  });

  it("does not run a plan whose checks failed", async () => {
    mockFetch({ [plans("finish")]: () => json(FINISH_NOT_READY) });
    open("finish", { database: "sales" });
    expect(
      await screen.findByText(
        "Some pre-flight checks failed: the plan cannot run. Fix what they say, then preview again.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText("sales is ONLINE, not RESTORING: nothing to finish"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Finish restore" })).toBeDisabled();
  });

  it("reports a task that fails, with what to do", async () => {
    const stream = sseStream();
    mockFetch({
      [plans("restore")]: () => json(RESTORE_PREVIEW),
      [execute("restore")]: () => stream.response,
    });
    open("restore", { database: "sales_copy", key: "sales.bak" }, { fields: "restore" });
    await userEvent.click(await screen.findByRole("button", { name: "Restore" }));
    stream.send("done", {
      ok: false,
      task: { id: 12, type: "RESTORE_DB", database: "sales_copy", percent: 0, lifecycle: "ERROR" },
    });
    stream.close();
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "The task ended as Failed. Read its log, fix the cause, then preview again.",
    );
    expect(screen.queryByRole("button", { name: "Open the report" })).toBeNull();
    // Back to a fresh preview of the same values.
    await userEvent.click(screen.getByRole("button", { name: "Preview again" }));
    expect(await screen.findByRole("button", { name: "Restore" })).toBeEnabled();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("keeps the task running when closed while following it", async () => {
    const stream = sseStream();
    const fetch = mockFetch({
      [plans("restore")]: () => json(RESTORE_PREVIEW),
      [execute("restore")]: () => stream.response,
    });
    const { onClose } = open(
      "restore",
      { database: "sales_copy", key: "sales.bak" },
      { fields: "restore" },
    );
    await userEvent.click(await screen.findByRole("button", { name: "Restore" }));
    const task = {
      id: 12,
      type: "RESTORE_DB",
      database: "sales_copy",
      percent: 5,
      lifecycle: "IN_PROGRESS",
    };
    stream.send("task", task);
    await screen.findByRole("progressbar");
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalledWith({ changed: true, running: task });
    expect(requestsTo(fetch, execute("restore"))[0].signal?.aborted).toBe(true);
  });

  it("follows the operation as a job: link to Activity, it keeps running, cancel through the job", async () => {
    const stream = sseStream();
    const fetch = mockFetch({
      [plans("restore")]: () => json(RESTORE_PREVIEW),
      [execute("restore")]: () => stream.response,
      "POST /api/v1/jobs/job-7/cancel": () => json({ id: "job-7", state: "running" }),
    });
    const { onClose } = open(
      "restore",
      { database: "sales_copy", key: "sales.bak" },
      { fields: "restore" },
    );
    await userEvent.click(await screen.findByRole("button", { name: "Restore" }));
    stream.send("job", { id: "job-7" });
    expect(await screen.findByText("It keeps running: follow it in Activity.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open in Activity" })).toHaveAttribute(
      "href",
      "#activity/job-7",
    );

    // Cancel goes through the job and the dialog keeps following.
    const cancel = screen.getByRole("button", { name: "Cancel operation" });
    await userEvent.click(cancel);
    await waitFor(() =>
      expect(requestsTo(fetch, "POST /api/v1/jobs/job-7/cancel")).toHaveLength(1),
    );
    expect(await screen.findByText(/Cancelling: the operation stops/)).toBeInTheDocument();
    expect(cancel).toHaveAttribute("aria-disabled", "true");
    await userEvent.click(cancel);
    expect(requestsTo(fetch, "POST /api/v1/jobs/job-7/cancel")).toHaveLength(1);

    // Closing while it runs hands the job to the page.
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalledWith({ changed: true, jobId: "job-7" });
  });

  it("keeps the link to the job after the run, from the done event", async () => {
    const stream = sseStream();
    mockFetch({
      [plans("drop")]: () => json(DROP_PREVIEW),
      [execute("drop")]: () => stream.response,
    });
    open("drop", { database: "sales" });
    await userEvent.type(
      await screen.findByRole("textbox", { name: "Type sales to confirm" }),
      "sales",
    );
    await userEvent.click(screen.getByRole("button", { name: "Drop database" }));
    stream.send("done", { ok: false, error: "boom", jobId: "job-9" });
    stream.close();
    expect(await screen.findByRole("link", { name: "Open in Activity" })).toHaveAttribute(
      "href",
      "#activity/job-9",
    );
    expect(screen.queryByText("It keeps running: follow it in Activity.")).toBeNull();
  });

  it("offers Activity for a refused Run that the engine records", async () => {
    mockFetch({
      [plans("drop")]: () => json(DROP_PREVIEW),
      [execute("drop")]: () => text("SQL password: not set\n", 422),
    });
    open("drop", { database: "sales" });
    await userEvent.type(
      await screen.findByRole("textbox", { name: "Type sales to confirm" }),
      "sales",
    );
    await userEvent.click(screen.getByRole("button", { name: "Drop database" }));
    expect(await screen.findByRole("link", { name: "See it in Activity" })).toHaveAttribute(
      "href",
      "#activity",
    );
  });

  it("closes on Escape and aborts the preview in flight", async () => {
    const answer = deferred<Response>();
    const fetch = mockFetch({ [plans("cancel")]: () => answer.promise });
    const { onClose } = open("cancel", { taskId: 7 });
    expect(screen.getByRole("dialog")).toHaveAccessibleName("Cancel task #7");
    // "Cancel" next to "Cancel task" would be ambiguous: the dismiss button says Close.
    expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledWith({ changed: false, running: undefined });
    expect(requestsTo(fetch, plans("cancel"))[0].signal?.aborted).toBe(true);
    answer.resolve(json(CANCEL_PREVIEW));
  });

  it("speaks Italian", async () => {
    mockFetch({ [plans("drop")]: () => json(DROP_PREVIEW) });
    open("drop", { database: "sales" }, { lang: "it" });
    expect(
      await screen.findByRole("textbox", { name: "Scrivi sales per confermare" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toHaveAccessibleName("Elimina sales");
    expect(screen.getByText("Controlli preliminari")).toBeInTheDocument();
    expect(screen.getByText("Eliminazione del database sales")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Elimina il database" })).toBeDisabled();
  });
});
