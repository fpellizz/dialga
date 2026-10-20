import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Database } from "./api";
import { DatabasesPage } from "./DatabasesPage";
import { CONFIG_PATH, DEV, renderI18n } from "./test/fixtures";
import { ENGINE, json, mockFetch, type Route, requestsTo, sseStream } from "./test/http";
import { chooseAction, openActions } from "./test/menu";
import { DROP_PREVIEW, REPORT, RESTORE_PREVIEW } from "./test/plans";

const TARGETS = "GET /api/v1/targets";
const DATABASES = "GET /api/v1/targets/dev-sql/databases";

const SALES: Database = {
  name: "sales",
  state: "ONLINE",
  recovery: "FULL",
  compatibility: 140,
  collation: "SQL_Latin1_General_CP1_CI_AS",
  owner: "rdsa",
  userAccess: "MULTI_USER",
  readOnly: false,
  createdAt: "2026-10-15T08:00:00",
  dataMB: 8,
  logMB: 2,
};
const STAGED: Database = { ...SALES, name: "staged", state: "RESTORING", owner: undefined };

function serve(routes: Record<string, Route> = {}) {
  return mockFetch({
    [TARGETS]: () => json({ configPath: CONFIG_PATH, targets: [DEV] }),
    [DATABASES]: () => json({ databases: [SALES, STAGED] }),
    ...routes,
  });
}

function row(name: string) {
  const cell = screen.getByRole("rowheader", { name: new RegExp(`^${name}`) });
  return within(cell.closest("tr") as HTMLElement);
}

describe("DatabasesPage actions", () => {
  it("offers the actions that fit the state of each database", async () => {
    serve();
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    await screen.findByRole("columnheader", { name: "Actions" });

    // Report stays on the row; the rest is in the menu.
    expect(row("sales").getByRole("button", { name: "Report of sales" })).toBeInTheDocument();
    expect(row("sales").queryByRole("button", { name: "Drop sales" })).toBeNull();
    await openActions("sales");
    const names = () => screen.getAllByRole("menuitem").map((i) => i.getAttribute("aria-label"));
    expect(names()).toEqual(["Export sales", "Access to sales", "Drop sales"]);
    await userEvent.keyboard("{Escape}");

    expect(row("staged").getByRole("button", { name: "Report of staged" })).toBeInTheDocument();
    await openActions("staged");
    expect(names()).toEqual(["Finish restore of staged", "Drop staged"]);
  });

  it("drops a database, then reloads the list and gives the focus back", async () => {
    const stream = sseStream();
    const fetch = serve({
      "POST /api/v1/targets/dev-sql/plans/drop": () => json(DROP_PREVIEW),
      "POST /api/v1/targets/dev-sql/plans/drop/execute": () => stream.response,
    });
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    await screen.findByRole("columnheader", { name: "Actions" });
    const drop = await chooseAction("sales", "Drop sales");

    const dialog = within(screen.getByRole("dialog", { name: "Drop sales" }));
    await userEvent.type(
      await dialog.findByRole("textbox", { name: "Type sales to confirm" }),
      "sales",
    );
    await userEvent.click(dialog.getByRole("button", { name: "Drop database" }));
    stream.send("step", {
      step: "drop",
      label: "Drop database sales",
      status: "ok",
      detail: "sales dropped",
    });
    stream.send("done", { ok: true });
    stream.close();
    expect(
      await dialog.findByText("sales was dropped.", { selector: ".banner-text" }),
    ).toBeInTheDocument();

    await userEvent.click(dialog.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(requestsTo(fetch, DATABASES)).toHaveLength(2));
    await waitFor(() => expect(drop).toHaveFocus());
  });

  it("closes the dialog with nothing run without reloading", async () => {
    const fetch = serve({ "POST /api/v1/targets/dev-sql/plans/drop": () => json(DROP_PREVIEW) });
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    await screen.findByRole("columnheader", { name: "Actions" });
    await chooseAction("sales", "Drop sales");
    await screen.findByRole("textbox", { name: "Type sales to confirm" });
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(requestsTo(fetch, DATABASES)).toHaveLength(1);
  });

  it("shows the post-restore report", async () => {
    serve({ "GET /api/v1/targets/dev-sql/databases/sales/report": () => json(REPORT) });
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    await screen.findByRole("columnheader", { name: "Actions" });
    await userEvent.click(row("sales").getByRole("button", { name: "Report of sales" }));

    const dialog = within(screen.getByRole("dialog", { name: "Report of sales" }));
    const checks = within(await dialog.findByRole("region", { name: "Checks" }));
    expect(checks.getByText("Login access")).toBeInTheDocument();
    expect(checks.getByText("Orphaned users")).toBeInTheDocument();
    expect(checks.getByText(/unusable on RDS without Active Directory/)).toBeInTheDocument();

    const props = within(dialog.getByRole("region", { name: "Properties" }));
    expect(props.getByText("SQL_Latin1_General_CP1_CI_AS")).toBeInTheDocument();
    expect(props.getByText("rdsa")).toBeInTheDocument();

    const files = within(dialog.getByRole("region", { name: "Files" }));
    expect(files.getByText("Unlimited")).toBeInTheDocument();
    expect(files.getByText("2 GiB")).toBeInTheDocument();
    expect(files.getByText("D:\\rdsdbdata\\DATA\\sales.mdf")).toBeInTheDocument();

    const orphans = within(dialog.getByRole("region", { name: "SQL users without a login" }));
    expect(orphans.getByText("app")).toBeInTheDocument();
    expect(orphans.getByText(/ALTER USER \[u\] WITH LOGIN = \[u\];/)).toBeInTheDocument();

    const objects = within(dialog.getByRole("region", { name: "Objects" }));
    expect(objects.getByText("USER_TABLE")).toBeInTheDocument();
    expect(objects.getByText("12")).toBeInTheDocument();

    await userEvent.click(dialog.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("restores from S3 and says when the task keeps running after closing", async () => {
    const stream = sseStream();
    serve({
      "POST /api/v1/targets/dev-sql/plans/restore": () => json(RESTORE_PREVIEW),
      "POST /api/v1/targets/dev-sql/plans/restore/execute": () => stream.response,
    });
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    await screen.findByRole("columnheader", { name: "Actions" });
    await userEvent.click(screen.getByRole("button", { name: "Restore from S3" }));
    const dialog = within(screen.getByRole("dialog", { name: "Restore from S3" }));
    await userEvent.type(dialog.getByRole("textbox", { name: "Database name" }), "sales_copy");
    await userEvent.type(
      dialog.getByRole("textbox", { name: "S3 key of the backup" }),
      "sales.bak",
    );
    await userEvent.click(dialog.getByRole("button", { name: "Preview" }));
    await userEvent.click(await dialog.findByRole("button", { name: "Restore" }));
    stream.send("task", {
      id: 12,
      type: "RESTORE_DB",
      database: "sales_copy",
      percent: 10,
      lifecycle: "IN_PROGRESS",
    });
    await dialog.findByRole("progressbar");
    await userEvent.click(dialog.getByRole("button", { name: "Close" }));

    const banner = await screen.findByText("Task #12 keeps running on dev-sql.");
    expect(banner.closest("[role=status]")).not.toBeNull();
    expect(screen.getByRole("link", { name: "Follow it in Instances" })).toHaveAttribute(
      "href",
      "#instances",
    );
    await userEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByText("Task #12 keeps running on dev-sql.")).toBeNull();
  });

  it("opens the report after a restore", async () => {
    const stream = sseStream();
    serve({
      "POST /api/v1/targets/dev-sql/plans/restore": () => json(RESTORE_PREVIEW),
      "POST /api/v1/targets/dev-sql/plans/restore/execute": () => stream.response,
      "GET /api/v1/targets/dev-sql/databases/sales_copy/report": () =>
        json({ ...REPORT, database: { ...REPORT.database, name: "sales_copy" } }),
    });
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    await screen.findByRole("columnheader", { name: "Actions" });
    await userEvent.click(screen.getByRole("button", { name: "Restore from S3" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Database name" }), "sales_copy");
    await userEvent.type(
      screen.getByRole("textbox", { name: "S3 key of the backup" }),
      "sales.bak",
    );
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));
    await userEvent.click(await screen.findByRole("button", { name: "Restore" }));
    stream.send("done", { ok: true });
    stream.close();
    await userEvent.click(await screen.findByRole("button", { name: "Open the report" }));
    expect(await screen.findByRole("dialog", { name: "Report of sales_copy" })).toBeInTheDocument();
    expect(await screen.findByRole("region", { name: "Objects" })).toBeInTheDocument();
  });
});
