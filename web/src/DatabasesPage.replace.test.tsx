import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Database } from "./api";
import { DatabasesPage } from "./DatabasesPage";
import type { PlanPreview } from "./plans";
import { CONFIG_PATH, DEV, renderI18n } from "./test/fixtures";
import { ENGINE, json, mockFetch, requestsTo, sseStream } from "./test/http";
import { DROP_PREVIEW, RESTORE_PREVIEW } from "./test/plans";

const TARGETS = "GET /api/v1/targets";
const DATABASES = "GET /api/v1/targets/dev-sql/databases";
const RESTORE = "POST /api/v1/targets/dev-sql/plans/restore";
const DROP = "POST /api/v1/targets/dev-sql/plans/drop";

const SALES: Database = {
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

const OLD = "sales_old_20261016T100000";

/** The engine's preview of a swap over sales: the replace check, the new actions, the name to retype. */
const SWAP_PREVIEW: PlanPreview = {
  ...RESTORE_PREVIEW,
  plan: {
    ...RESTORE_PREVIEW.plan,
    actions: [
      {
        ...RESTORE_PREVIEW.plan.actions[0],
        title:
          "Restore sales_dialga_new_20261016100000 from s3://b/import/sales.bak (temporary name)",
      },
      {
        id: "single-user",
        title: "Close sales to other sessions (disconnects them)",
        kind: "sql",
        risk: "destructive",
        statement: "ALTER DATABASE [sales] SET SINGLE_USER;",
      },
      {
        id: "rename-old",
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
  confirmation: "sales",
  replace: { strategy: "swap", stamp: "20261016100000", old: OLD },
  request: { database: "sales", key: "import/sales.bak", replace: "swap", stamp: "20261016100000" },
};

function serve(routes: Record<string, () => Response>) {
  return mockFetch({
    [TARGETS]: () => json({ configPath: CONFIG_PATH, targets: [DEV] }),
    [DATABASES]: () => json({ databases: [SALES] }),
    ...routes,
  });
}

async function fillRestore(database: string) {
  renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
  await screen.findByRole("columnheader", { name: "Actions" });
  await userEvent.click(screen.getByRole("button", { name: "Restore from S3" }));
  await userEvent.type(screen.getByRole("textbox", { name: "Database name" }), database);
  await userEvent.type(screen.getByRole("textbox", { name: "S3 key of the backup" }), "sales.bak");
}

const body = (init: RequestInit) => JSON.parse(String(init.body));

describe("Restore from S3 over an existing database", () => {
  it("offers nothing for a new name", async () => {
    serve({});
    await fillRestore("sales_copy");
    expect(screen.queryByRole("radiogroup")).toBeNull();
  });

  it("stops by default: the request carries no replace", async () => {
    const fetch = serve({ [RESTORE]: () => json(RESTORE_PREVIEW) });
    await fillRestore("sales");
    expect(await screen.findByRole("radio", { name: "Stop" })).toBeChecked();
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));
    await screen.findByRole("button", { name: "Restore" });
    expect(body(requestsTo(fetch, RESTORE)[0])).toEqual({ database: "sales", key: "sales.bak" });
  });

  it("sends drop without the safety backup when it is switched off", async () => {
    const fetch = serve({ [RESTORE]: () => json(RESTORE_PREVIEW) });
    await fillRestore("sales");
    await userEvent.click(await screen.findByRole("radio", { name: "Drop it and restore" }));
    await userEvent.click(
      screen.getByRole("checkbox", { name: "Back up the current database to S3 first" }),
    );
    expect(screen.getByRole("alert")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));
    await screen.findByRole("button", { name: "Restore" });
    expect(body(requestsTo(fetch, RESTORE)[0])).toEqual({
      database: "sales",
      key: "sales.bak",
      replace: "drop",
      backupFirst: false,
    });
  });

  it("swaps: previews the choice, asks for the name, executes, then offers to drop the old one", async () => {
    const stream = sseStream();
    const fetch = serve({
      [RESTORE]: () => json(SWAP_PREVIEW),
      [`${RESTORE}/execute`]: () => stream.response,
      [DROP]: () => json({ ...DROP_PREVIEW, request: { database: OLD }, confirmation: OLD }),
    });
    await fillRestore("sales");
    await userEvent.click(await screen.findByRole("radio", { name: "Keep the old one (swap)" }));
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));

    const checks = within(await screen.findByRole("region", { name: "Pre-flight checks" }));
    expect(checks.getByText("Replace the existing database")).toBeInTheDocument();
    expect(checks.getByText(/swap: two copies/)).toBeInTheDocument();
    expect(checks.getByText("3 sessions on sales")).toBeInTheDocument();
    expect(body(requestsTo(fetch, RESTORE)[0])).toEqual({
      database: "sales",
      key: "sales.bak",
      replace: "swap",
    });

    const actions = within(screen.getByRole("region", { name: "What runs" }));
    expect(
      actions.getByText("Restore sales beside the current one (temporary name)"),
    ).toBeInTheDocument();
    expect(
      actions.getByText("Close sales to other sessions (disconnects them)"),
    ).toBeInTheDocument();
    expect(
      actions.getByText("Rename sales to its old name (the current database is kept)"),
    ).toBeInTheDocument();
    const run = screen.getByRole("button", { name: "Restore" });
    expect(run).toBeDisabled();
    await userEvent.type(screen.getByRole("textbox", { name: "Type sales to confirm" }), "sales");
    await userEvent.click(run);
    expect(body(requestsTo(fetch, `${RESTORE}/execute`)[0])).toMatchObject({
      database: "sales",
      replace: "swap",
      stamp: "20261016100000",
      confirm: "sales",
    });

    stream.send("step", {
      step: "swap",
      label: "Swap the databases",
      status: "ok",
    });
    stream.send("done", { ok: true, replace: { strategy: "swap", oldDatabase: OLD } });
    stream.close();
    const outcome = within(await screen.findByRole("region", { name: "Result of the replace" }));
    expect(outcome.getByText(`The previous database was kept as ${OLD}.`)).toBeInTheDocument();

    await userEvent.click(outcome.getByRole("button", { name: `Drop ${OLD}` }));
    expect(await screen.findByRole("dialog", { name: `Drop ${OLD}` })).toBeInTheDocument();
    await waitFor(() => expect(requestsTo(fetch, DROP)).toHaveLength(1));
    expect(body(requestsTo(fetch, DROP)[0])).toEqual({ database: OLD });
  });

  it("hides the choice for a differential restore", async () => {
    serve({});
    await fillRestore("sales");
    await screen.findByRole("radiogroup");
    await userEvent.click(screen.getByRole("checkbox", { name: /^Differential backup/ }));
    expect(screen.queryByRole("radiogroup")).toBeNull();
  });

  it("names the safety backup a drop took", async () => {
    const stream = sseStream();
    serve({
      [RESTORE]: () =>
        json({
          ...SWAP_PREVIEW,
          replace: { strategy: "drop" },
          request: { ...SWAP_PREVIEW.request, replace: "drop" },
        }),
      [`${RESTORE}/execute`]: () => stream.response,
    });
    await fillRestore("sales");
    await userEvent.click(await screen.findByRole("radio", { name: "Drop it and restore" }));
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));
    await userEvent.type(
      await screen.findByRole("textbox", { name: "Type sales to confirm" }),
      "sales",
    );
    await userEvent.click(screen.getByRole("button", { name: "Restore" }));
    stream.send("step", {
      step: "backup",
      label: "Back up sales",
      status: "ok",
      detail: "import/sales_before_replace_20261016T100000.bak",
    });
    stream.send("done", { ok: true });
    stream.close();
    expect(
      await screen.findByText(
        "s3://my-sqlserver-backups/import/sales_before_replace_20261016T100000.bak",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Drop sales_old/ })).toBeNull();
  });
});
