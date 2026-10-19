import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Database } from "./api";
import { DatabasesPage } from "./DatabasesPage";
import { ACCESS, accessPreview, LOGINS } from "./test/access";
import { CONFIG_PATH, DEV, renderI18n, renderWithPassword } from "./test/fixtures";
import { ENGINE, json, mockFetch, type Route, requestsTo, sseStream, text } from "./test/http";

const TARGETS = "GET /api/v1/targets";
const DATABASES = "GET /api/v1/targets/dev-sql/databases";
const ACCESS_URL = "GET /api/v1/targets/dev-sql/databases/sales/access";
const LOGINS_URL = "GET /api/v1/targets/dev-sql/logins";
const plan = (op: string) => `POST /api/v1/targets/dev-sql/plans/${op}`;
const execute = (op: string) => `${plan(op)}/execute`;

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

function serve(routes: Record<string, Route> = {}) {
  return mockFetch({
    [TARGETS]: () => json({ configPath: CONFIG_PATH, targets: [DEV] }),
    [DATABASES]: () => json({ databases: [SALES] }),
    [ACCESS_URL]: () => json(ACCESS),
    [LOGINS_URL]: () => json({ logins: LOGINS }),
    ...routes,
  });
}

async function openPanel() {
  await screen.findByRole("columnheader", { name: "Actions" });
  await userEvent.click(screen.getByRole("button", { name: "Access to sales" }));
  const dialog = screen.getByRole("dialog", { name: "Access to sales" });
  await within(dialog).findByRole("table", { name: "Users of sales" });
  return within(dialog);
}

/** The body of a request, as sent. */
function bodyOf(init: RequestInit): Record<string, unknown> {
  return JSON.parse(String(init.body));
}

const STEP = (label: string, status: "running" | "ok") => ({
  step: "add-role",
  label,
  status,
});

describe("the access panel", () => {
  it("lists the users: orphans with a fix, users Dialga never changes read-only", async () => {
    serve();
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    const panel = await openPanel();

    const rows = panel.getAllByRole("row").slice(1);
    expect(rows).toHaveLength(3);
    const dbo = within(rows[0] as HTMLElement);
    expect(dbo.getByText("Managed by SQL Server or RDS")).toBeInTheDocument();
    expect(dbo.queryByRole("button")).toBeNull();

    const app = within(rows[1] as HTMLElement);
    expect(app.getAllByText("app_rw")).toHaveLength(2); // user and login
    expect(app.getByText("Read & write")).toBeInTheDocument();
    expect(app.getByText("Yes")).toBeInTheDocument();
    expect(app.getByText("db_datareader, db_datawriter, db_backupoperator")).toBeInTheDocument();
    expect(app.getByRole("button", { name: "Revoke access of app_rw" })).toBeInTheDocument();
    expect(app.queryByRole("button", { name: /Fix orphan/ })).toBeNull();

    const orphan = within(rows[2] as HTMLElement);
    expect(rows[2]).toHaveClass("row-orphan");
    expect(orphan.getByText(/Orphan/)).toBeInTheDocument();
    expect(orphan.getByText("no login")).toBeInTheDocument();
    expect(orphan.getByRole("button", { name: "Fix orphan legacy" })).toBeInTheDocument();
    expect(orphan.queryByRole("button", { name: /Revoke/ })).toBeNull();

    expect(panel.getByText(/2 Windows users or groups are not listed/)).toBeInTheDocument();
  });

  it("shows a refused read like elsewhere, and the 428 through the password banner", async () => {
    serve({
      [ACCESS_URL]: () =>
        new Response("SQL password needed for target dev-sql\n", {
          status: 428,
          headers: { "Dialga-Password-Target": "dev-sql" },
        }),
    });
    renderWithPassword(<DatabasesPage engine={ENGINE} engineError="" />);
    await screen.findByRole("columnheader", { name: "Actions" });
    await userEvent.click(screen.getByRole("button", { name: "Access to sales" }));
    const dialog = within(screen.getByRole("dialog", { name: "Access to sales" }));
    expect(await dialog.findByText("The SQL password for dev-sql is needed")).toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "Enter password for dev-sql" })).toBeInTheDocument();
  });

  it("shows a 502 from the engine", async () => {
    serve({ [ACCESS_URL]: () => text("cannot reach the instance\n", 502) });
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    await screen.findByRole("columnheader", { name: "Actions" });
    await userEvent.click(screen.getByRole("button", { name: "Access to sales" }));
    expect(await screen.findByText("cannot reach the instance")).toBeInTheDocument();
  });
});

describe("grant", () => {
  it("grants an existing login: the master login is not selectable, duplicate steps stay apart, the panel reloads", async () => {
    const stream = sseStream();
    const request = { database: "sales", login: "reporter", level: "read" as const };
    const fetch = serve({
      [plan("grant")]: () => json(accessPreview("grant", request)),
      [execute("grant")]: () => stream.response,
    });
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    const panel = await openPanel();
    await userEvent.click(panel.getByRole("button", { name: "Grant access" }));

    const dialog = within(screen.getByRole("dialog", { name: "Grant access in sales" }));
    const select = await dialog.findByRole("combobox", { name: "Login" });
    await waitFor(() => expect(select).toBeEnabled());
    expect(dialog.getByRole("option", { name: /admin \(master user/ })).toBeDisabled();
    expect(dialog.getByRole("button", { name: "Preview" })).toBeDisabled();
    await userEvent.selectOptions(select, "reporter");
    await userEvent.click(dialog.getByRole("button", { name: "Preview" }));

    // The preview: both actions are listed although they share an id.
    expect(await dialog.findByText("Add app_rw to db_datareader")).toBeInTheDocument();
    expect(dialog.getByText("Add app_rw to db_datawriter")).toBeInTheDocument();
    expect(bodyOf(requestsTo(fetch, plan("grant"))[0] as RequestInit)).toEqual(request);

    await userEvent.click(dialog.getByRole("button", { name: "Grant access" }));
    expect(bodyOf(requestsTo(fetch, execute("grant"))[0] as RequestInit)).toEqual({
      ...request,
      digest: "a".repeat(32),
    });
    stream.send("job", { id: "j1" });
    stream.send("step", STEP("Add reporter to db_datareader", "running"));
    stream.send("step", STEP("Add reporter to db_datareader", "ok"));
    stream.send("step", STEP("Add reporter to db_datawriter", "running"));
    const steps = within(await dialog.findByRole("region", { name: "Progress" }));
    await waitFor(() => expect(steps.getAllByRole("listitem")).toHaveLength(2));
    expect(steps.getAllByRole("listitem")[0]).toHaveTextContent("Add reporter to db_datareader");
    expect(steps.getAllByRole("listitem")[0]).toHaveTextContent("Passed");
    expect(steps.getAllByRole("listitem")[1]).toHaveTextContent("Running");
    stream.send("step", STEP("Add reporter to db_datawriter", "ok"));
    stream.send("done", { ok: true, jobId: "j1" });
    stream.close();
    expect(
      await dialog.findByText("reporter now has access to sales.", { selector: ".banner-text" }),
    ).toBeInTheDocument();
    expect(dialog.getByRole("link", { name: "Open in Activity" })).toBeInTheDocument();

    await userEvent.click(dialog.getByRole("button", { name: "Close" }));
    await waitFor(() => expect(requestsTo(fetch, ACCESS_URL)).toHaveLength(2));
    expect(screen.getByRole("dialog", { name: "Access to sales" })).toBeInTheDocument();
  });

  it("creates a login: the generated password goes to execute only", async () => {
    const stream = sseStream();
    const request = {
      database: "sales",
      login: "newuser",
      level: "readwrite" as const,
      backup: true,
      createLogin: true,
    };
    const fetch = serve({
      [plan("grant")]: () => json(accessPreview("grant", request)),
      [execute("grant")]: () => stream.response,
    });
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    const panel = await openPanel();
    await userEvent.click(panel.getByRole("button", { name: "Grant access" }));
    const dialog = within(screen.getByRole("dialog", { name: "Grant access in sales" }));

    await userEvent.click(dialog.getByRole("radio", { name: "New login" }));
    await userEvent.type(dialog.getByRole("textbox", { name: "New login name" }), "newuser");
    // A weak password does not pass: no preview.
    const password = dialog.getByLabelText("Password");
    await userEvent.type(password, "weak");
    expect(dialog.getByText("The password needs 12 to 128 characters.")).toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "Preview" })).toBeDisabled();

    await userEvent.click(dialog.getByRole("button", { name: "Generate" }));
    const generated = (password as HTMLInputElement).value;
    expect(generated).toHaveLength(20);
    expect(password).toHaveAttribute("type", "text");
    expect(dialog.getByText(/Dialga never stores this password/)).toBeInTheDocument();

    await userEvent.click(dialog.getByRole("radio", { name: "Read & write" }));
    await userEvent.click(dialog.getByRole("checkbox", { name: "Can back up" }));
    await userEvent.click(dialog.getByRole("button", { name: "Preview" }));
    await dialog.findByText("Add app_rw to db_datareader");

    const previewBody = String(requestsTo(fetch, plan("grant"))[0]?.body);
    expect(previewBody).not.toContain(generated);
    expect(previewBody).not.toContain("loginPassword");
    expect(JSON.parse(previewBody)).toEqual(request);

    await userEvent.click(dialog.getByRole("button", { name: "Grant access" }));
    const executed = requestsTo(fetch, execute("grant"));
    expect(executed).toHaveLength(1);
    expect(bodyOf(executed[0] as RequestInit)).toEqual({
      ...request,
      digest: "a".repeat(32),
      loginPassword: generated,
    });
    // Nowhere else: no other request carries it, and the dialog keeps it out of the URL.
    for (const [input, init] of fetch.mock.calls) {
      if (!String(input).endsWith("/execute")) {
        expect(`${String(input)}${String(init?.body ?? "")}`).not.toContain(generated);
      }
    }
    stream.send("done", { ok: true });
    stream.close();
    expect(await dialog.findByText(/Dialga did not keep it/)).toBeInTheDocument();
    await userEvent.click(dialog.getByRole("button", { name: "Close" }));
    expect(screen.queryByDisplayValue(generated)).toBeNull();
  });

  it("shows a refused execute and keeps the password out of the message", async () => {
    const request = {
      database: "sales",
      login: "newuser",
      level: "read" as const,
      createLogin: true,
    };
    serve({
      [plan("grant")]: () => json(accessPreview("grant", request)),
      [execute("grant")]: () => text("invalid password for the new login: ****\n", 422),
    });
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    const panel = await openPanel();
    await userEvent.click(panel.getByRole("button", { name: "Grant access" }));
    const dialog = within(screen.getByRole("dialog", { name: "Grant access in sales" }));
    await userEvent.click(dialog.getByRole("radio", { name: "New login" }));
    await userEvent.type(dialog.getByRole("textbox", { name: "New login name" }), "newuser");
    await userEvent.type(dialog.getByLabelText("Password"), "Str0ng-enough-pw");
    await userEvent.click(dialog.getByRole("button", { name: "Preview" }));
    await dialog.findByText("Add app_rw to db_datareader");
    await userEvent.click(dialog.getByRole("button", { name: "Grant access" }));
    const alert = await dialog.findByRole("alert");
    expect(alert).toHaveTextContent("invalid password for the new login");
    expect(alert).not.toHaveTextContent("Str0ng-enough-pw");
  });
});

describe("revoke and fix orphan", () => {
  it("removes a user: destructive, retyping the login", async () => {
    const stream = sseStream();
    const request = { database: "sales", login: "app_rw", remove: "user" as const };
    const fetch = serve({
      [plan("revoke")]: () =>
        json(
          accessPreview("revoke", request, {
            confirmation: "app_rw",
            plan: {
              ...accessPreview("revoke", request).plan,
              actions: [
                {
                  id: "drop-user",
                  title: "Drop user app_rw from sales",
                  kind: "sql",
                  risk: "destructive",
                  statement: "DROP USER [app_rw];",
                },
              ],
            },
          }),
        ),
      [execute("revoke")]: () => stream.response,
    });
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    const panel = await openPanel();
    await userEvent.click(panel.getByRole("button", { name: "Revoke access of app_rw" }));
    const choice = within(await screen.findByRole("region", { name: "Revoke access of app_rw" }));
    await waitFor(() =>
      expect(choice.getByRole("radio", { name: "Level of access" })).toHaveFocus(),
    );
    await userEvent.click(choice.getByRole("radio", { name: "Remove the user" }));
    await userEvent.click(choice.getByRole("button", { name: "Preview" }));

    expect(bodyOf(requestsTo(fetch, plan("revoke"))[0] as RequestInit)).toEqual(request);
    const dialog = within(screen.getByRole("dialog", { name: "Revoke access in sales" }));
    const confirm = await dialog.findByRole("textbox", { name: "Type app_rw to confirm" });
    expect(dialog.getByText(/removes the user from sales/)).toBeInTheDocument();
    expect(dialog.getByText("Destructive")).toBeInTheDocument();
    const run = dialog.getByRole("button", { name: "Revoke" });
    expect(run).toBeDisabled();
    await userEvent.type(confirm, "app_rw");
    expect(run).toBeEnabled();
    expect(run).toHaveClass("btn-destructive");
    await userEvent.click(run);
    expect(bodyOf(requestsTo(fetch, execute("revoke"))[0] as RequestInit)).toEqual({
      ...request,
      digest: "a".repeat(32),
      confirm: "app_rw",
    });
    stream.send("done", { ok: true });
    stream.close();
    expect(
      await dialog.findByText("The user of app_rw was removed from sales.", {
        selector: ".banner-text",
      }),
    ).toBeInTheDocument();
    await userEvent.click(dialog.getByRole("button", { name: "Close" }));
    await waitFor(() => expect(requestsTo(fetch, ACCESS_URL)).toHaveLength(2));
    expect(screen.queryByRole("region", { name: "Revoke access of app_rw" })).toBeNull();
  });

  it("only offers what the user has to take back", async () => {
    serve();
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    const panel = await openPanel();
    await userEvent.click(panel.getByRole("button", { name: "Revoke access of app_rw" }));
    const choice = within(await screen.findByRole("region", { name: "Revoke access of app_rw" }));
    expect(choice.getByRole("radio", { name: "Permission to back up" })).toBeEnabled();
    await userEvent.keyboard("{Escape}");
    // Escape closed the question, not the panel.
    expect(screen.queryByRole("region", { name: "Revoke access of app_rw" })).toBeNull();
    expect(screen.getByRole("dialog", { name: "Access to sales" })).toBeInTheDocument();
  });

  it("fixes an orphan", async () => {
    const stream = sseStream();
    const request = { database: "sales", user: "legacy" };
    const fetch = serve({
      [plan("fix-orphan")]: () => json(accessPreview("fix-orphan", request)),
      [execute("fix-orphan")]: () => stream.response,
    });
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    const panel = await openPanel();
    await userEvent.click(panel.getByRole("button", { name: "Fix orphan legacy" }));
    const dialog = within(screen.getByRole("dialog", { name: "Fix orphan legacy in sales" }));
    await dialog.findByText("Add app_rw to db_datareader");
    expect(bodyOf(requestsTo(fetch, plan("fix-orphan"))[0] as RequestInit)).toEqual(request);
    await userEvent.click(dialog.getByRole("button", { name: "Fix orphan" }));
    expect(bodyOf(requestsTo(fetch, execute("fix-orphan"))[0] as RequestInit)).toEqual({
      ...request,
      digest: "a".repeat(32),
    });
    stream.send("done", { ok: true });
    stream.close();
    expect(
      await dialog.findByText("User legacy is bound to its login again in sales.", {
        selector: ".banner-text",
      }),
    ).toBeInTheDocument();
    await userEvent.click(dialog.getByRole("button", { name: "Close" }));
    await waitFor(() => expect(requestsTo(fetch, ACCESS_URL)).toHaveLength(2));
  });

  it("shows a 412 from the preview as an error with its hint", async () => {
    serve({
      [plan("fix-orphan")]: () => text("legacy is not an orphan\n", 412),
    });
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    const panel = await openPanel();
    await userEvent.click(panel.getByRole("button", { name: "Fix orphan legacy" }));
    const dialog = within(screen.getByRole("dialog", { name: "Fix orphan legacy in sales" }));
    expect(await dialog.findByText("legacy is not an orphan")).toBeInTheDocument();
  });
});
