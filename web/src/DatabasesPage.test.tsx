import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import type { Database } from "./api";
import { DatabasesPage } from "./DatabasesPage";
import { CONFIG_PATH, DEV, database, PROD, renderI18n } from "./test/fixtures";
import { deferred, ENGINE, json, mockFetch, type Route, requestsTo, text } from "./test/http";

const TARGETS = "GET /api/v1/targets";
const DEV_DBS = "GET /api/v1/targets/dev-sql/databases";
const PROD_DBS = "GET /api/v1/targets/prod-sql/databases";
const REMEMBERED = "dialga.databases.target";

const SALES = database("sales", { dataMB: 2048, logMB: 512 });
const HR = database("hr", { recovery: "SIMPLE", compatibility: 130, owner: undefined });
const ARCHIVE = database("archive", {
  state: "OFFLINE",
  readOnly: true,
  userAccess: "SINGLE_USER",
  dataMB: 4,
  logMB: 4,
});

function targets(...list: (typeof DEV)[]): Route {
  return () => json({ configPath: CONFIG_PATH, targets: list });
}

function databases(...list: Database[]): Route {
  return () => json({ databases: list });
}

function page() {
  return renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
}

function selector() {
  return screen.getByRole("combobox", { name: "Instance" }) as HTMLSelectElement;
}

/** The database names, in the order of the table. */
function names() {
  return screen.getAllByRole("rowheader").map((cell) => cell.querySelector(".mono")?.textContent);
}

/** The page Refresh: its name says which databases (the visible text is just "Refresh"). */
function pageRefresh() {
  return within(screen.getByRole("banner")).getByRole("button", {
    name: /^Refresh the databases of /,
  });
}

const ENGINE_HINT =
  "Restart Dialga. If it happens again, start it from a terminal to read the engine log.";

describe("DatabasesPage", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/");
    // A remembered target that no longer exists: the page starts on the first one.
    // (Set explicitly: databases.ts also keeps the last choice in memory.)
    localStorage.setItem(REMEMBERED, "gone-sql");
  });

  it("shows a skeleton, then the databases of the first target", async () => {
    const answer = deferred<Response>();
    const fetch = mockFetch({ [TARGETS]: targets(DEV, PROD), [DEV_DBS]: () => answer.promise });
    const { container } = page();

    const card = await screen.findByRole("region", { name: "User databases on dev-sql" });
    expect(selector().value).toBe("dev-sql");
    // Busy from its first render, before the request starts.
    expect(card).toHaveAttribute("aria-busy", "true");
    expect(container.querySelectorAll(".skeleton")).toHaveLength(3);
    expect(screen.getByRole("searchbox", { name: "Filter" })).toBeDisabled();
    expect(pageRefresh()).toHaveAccessibleName("Refresh the databases of dev-sql");
    // Busy, not disabled: it keeps the focus and ignores clicks.
    expect(pageRefresh()).toHaveAttribute("aria-disabled", "true");
    await userEvent.click(pageRefresh());
    expect(requestsTo(fetch, DEV_DBS)).toHaveLength(1);

    answer.resolve(json({ databases: [SALES, HR, ARCHIVE] }));
    expect(await screen.findByRole("table", { name: "User databases on dev-sql" })).toBeVisible();
    expect(card).toHaveAttribute("aria-busy", "false");
    expect(container.querySelector(".skeleton")).toBeNull();
    expect(names()).toEqual(["archive", "hr", "sales"]);
    expect(screen.getByText("3 databases")).toBeInTheDocument();
    expect(pageRefresh()).not.toHaveAttribute("aria-disabled");
  });

  it("shows state, size, compatibility, recovery, owner and access of each database", async () => {
    mockFetch({ [TARGETS]: targets(DEV), [DEV_DBS]: databases(SALES, HR, ARCHIVE) });
    page();
    await screen.findByRole("table");
    const row = (name: string) =>
      screen.getByRole("rowheader", { name: new RegExp(`^${name}`) }).closest("tr") as HTMLElement;

    const sales = within(row("sales"));
    expect(sales.getByText("Online")).toHaveClass("badge-ok");
    expect(sales.getByText("2.5 GiB")).toHaveAttribute("title", "Data 2 GiB · log 512 MiB");
    expect(sales.getByText("140").parentElement).toHaveTextContent("140 · 2017");
    expect(sales.getByText("FULL")).toBeInTheDocument();
    expect(sales.getByText("admin")).toBeInTheDocument();

    const hr = within(row("hr"));
    expect(hr.getByText("130").parentElement).toHaveTextContent("130 · 2016");
    expect(hr.getByText("SIMPLE")).toBeInTheDocument();
    // A screen reader reads the reason, not the dash.
    const noOwner = hr.getByText("No login has the owner SID");
    expect(noOwner).toHaveClass("sr-only");
    expect(noOwner.parentElement).toHaveAttribute("title", "No login has the owner SID");
    expect(hr.getByText("—")).toHaveAttribute("aria-hidden", "true");

    const archive = within(row("archive"));
    expect(archive.getByText("Offline")).toHaveClass("badge-skipped");
    expect(archive.getByText("Read-only")).toBeInTheDocument();
    expect(archive.getByText("Single user")).toHaveClass("badge-warning");
  });

  it("switches target and remembers it, in the address too", async () => {
    const fetch = mockFetch({
      [TARGETS]: targets(DEV, PROD),
      [DEV_DBS]: databases(SALES),
      [PROD_DBS]: databases(HR, ARCHIVE),
    });
    const { unmount } = page();
    expect(await screen.findByText("1 database")).toBeInTheDocument();

    await userEvent.selectOptions(selector(), "prod-sql");
    expect(await screen.findByRole("table", { name: "User databases on prod-sql" })).toBeVisible();
    expect(names()).toEqual(["archive", "hr"]);
    expect(localStorage.getItem(REMEMBERED)).toBe("prod-sql");
    expect(window.location.hash).toBe("#databases/prod-sql");
    expect(requestsTo(fetch, PROD_DBS)).toHaveLength(1);

    unmount();
    window.history.replaceState(null, "", "/#databases");
    page();
    expect(await screen.findByRole("table", { name: "User databases on prod-sql" })).toBeVisible();
    expect(selector().value).toBe("prod-sql");
  });

  it("opens the target of a #databases/<target> link before the remembered one", async () => {
    localStorage.setItem(REMEMBERED, "dev-sql");
    window.history.replaceState(null, "", "/#databases/prod-sql");
    const fetch = mockFetch({
      [TARGETS]: targets(DEV, PROD),
      [DEV_DBS]: databases(SALES),
      [PROD_DBS]: databases(HR),
    });
    page();
    expect(await screen.findByRole("table", { name: "User databases on prod-sql" })).toBeVisible();
    expect(localStorage.getItem(REMEMBERED)).toBe("prod-sql");
    expect(requestsTo(fetch, DEV_DBS)).toHaveLength(0);
  });

  it("falls back to the remembered target when the link names an unknown one", async () => {
    localStorage.setItem(REMEMBERED, "prod-sql");
    window.history.replaceState(null, "", "/#databases/gone-sql");
    mockFetch({
      [TARGETS]: targets(DEV, PROD),
      [PROD_DBS]: databases(HR),
    });
    page();
    expect(await screen.findByRole("table", { name: "User databases on prod-sql" })).toBeVisible();
    expect(localStorage.getItem(REMEMBERED)).toBe("prod-sql");
  });

  it("filters with a live count and clears the filter", async () => {
    mockFetch({ [TARGETS]: targets(DEV), [DEV_DBS]: databases(SALES, HR, ARCHIVE) });
    page();
    await screen.findByRole("table");
    const filter = screen.getByRole("searchbox", { name: "Filter" });

    await userEvent.type(filter, "SIMPLE");
    expect(names()).toEqual(["hr"]);
    expect(screen.getByText("1 of 3 databases")).toBeInTheDocument();

    await userEvent.clear(filter);
    await userEvent.type(filter, "offline");
    expect(names()).toEqual(["archive"]);

    await userEvent.type(filter, "-nothing");
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.getByRole("heading", { name: "No databases match the filter" })).toBeVisible();
    expect(screen.getByText("0 of 3 databases")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Clear filter" }));
    expect(filter).toHaveValue("");
    expect(names()).toEqual(["archive", "hr", "sales"]);
    expect(screen.getByText("3 databases")).toBeInTheDocument();
  });

  it("sorts from the keyboard and says so with aria-sort", async () => {
    mockFetch({ [TARGETS]: targets(DEV), [DEV_DBS]: databases(SALES, HR, ARCHIVE) });
    page();
    await screen.findByRole("table");
    const header = (name: string) => screen.getByRole("columnheader", { name });
    expect(header("Name")).toHaveAttribute("aria-sort", "ascending");
    expect(header("Size")).not.toHaveAttribute("aria-sort");

    // Size starts with the largest database.
    within(header("Size")).getByRole("button").focus();
    await userEvent.keyboard("{Enter}");
    expect(header("Size")).toHaveAttribute("aria-sort", "descending");
    expect(header("Name")).not.toHaveAttribute("aria-sort");
    expect(names()).toEqual(["sales", "hr", "archive"]);

    await userEvent.keyboard(" ");
    expect(header("Size")).toHaveAttribute("aria-sort", "ascending");
    expect(names()).toEqual(["archive", "hr", "sales"]);

    await userEvent.tab();
    await userEvent.keyboard("{Enter}");
    expect(header("Compatibility")).toHaveAttribute("aria-sort", "ascending");
    expect(names()).toEqual(["hr", "archive", "sales"]);
  });

  it("shows the empty state of an instance with only system databases", async () => {
    mockFetch({ [TARGETS]: targets(DEV), [DEV_DBS]: databases() });
    page();
    expect(await screen.findByRole("heading", { name: "No user databases" })).toBeVisible();
    expect(screen.getByText("0 databases")).toBeInTheDocument();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it.each([
    [
      422,
      "SQL password: sql.password is prompt: set DIALGA_SQL_PASSWORD",
      // No hint of its own: what the list needs, by how the target connects (DEV: its own tunnel).
      "The list is read with a SQL session: the engine needs the SQL password and the tunnel you opened on the port of the target (open it, then retry).",
    ],
    [
      502,
      "failed to get shared config profile, dev",
      "Create the profile dev in ~/.aws/config (aws configure sso), or fix aws_profile in the configuration.",
    ],
  ])("shows the engine's %i with what to do, then retries", async (status, message, hint) => {
    const fetch = mockFetch({
      [TARGETS]: targets(DEV),
      [DEV_DBS]: (_req, call) =>
        call === 0 ? text(`${message}\n`, status) : json({ databases: [SALES] }),
    });
    page();
    const banner = await screen.findByRole("alert");
    expect(banner.querySelector(".banner-text")).toHaveTextContent(new RegExp(`^${message}$`));
    expect(banner.querySelector(".banner-hint")).toHaveTextContent(hint);
    expect(screen.queryByRole("region", { name: "User databases on dev-sql" })).toBeNull();

    await userEvent.click(pageRefresh());
    expect(await screen.findByRole("table")).toBeVisible();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(requestsTo(fetch, DEV_DBS)).toHaveLength(2);
  });

  it("does not show the databases of the previous target while the next one loads", async () => {
    const prod = deferred<Response>();
    mockFetch({
      [TARGETS]: targets(DEV, PROD),
      [DEV_DBS]: databases(SALES),
      [PROD_DBS]: () => prod.promise,
    });
    const { container } = page();
    await screen.findByRole("table", { name: "User databases on dev-sql" });

    await userEvent.selectOptions(selector(), "prod-sql");
    expect(screen.queryByRole("table")).toBeNull();
    expect(container.querySelectorAll(".skeleton")).toHaveLength(3);

    prod.resolve(json({ databases: [HR] }));
    expect(await screen.findByRole("table", { name: "User databases on prod-sql" })).toBeVisible();
    expect(names()).toEqual(["hr"]);
  });

  it("points to Instances when no target is configured", async () => {
    const fetch = mockFetch({ [TARGETS]: targets() });
    page();
    const empty = await screen.findByRole("status");
    expect(within(empty).getByRole("heading", { name: "No instances configured" })).toBeVisible();
    expect(within(empty).getByRole("link", { name: "Go to Instances" })).toHaveAttribute(
      "href",
      "#instances",
    );
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it("shows the errors of the configuration", async () => {
    mockFetch({
      [TARGETS]: () =>
        json({ configPath: CONFIG_PATH, error: "invalid YAML: line 2", targets: [] }),
    });
    page();
    const banner = await screen.findByRole("alert");
    expect(within(banner).getByText("The configuration file has errors")).toBeVisible();
    expect(banner.querySelector("pre")).toHaveTextContent("invalid YAML: line 2");
    expect(banner).toHaveTextContent(
      "Fix the configuration file (its path is in Settings), then refresh.",
    );
    expect(screen.queryByText("No instances configured")).toBeNull();
  });

  it("shows a failed target list without the Error: prefix", async () => {
    mockFetch({ [TARGETS]: () => text("unauthorized\n", 401) });
    page();
    const banner = await screen.findByRole("alert");
    expect(banner.querySelector(".banner-text")).toHaveTextContent(/^unauthorized$/);
    // 401: the session token is not the engine's, as after a restart.
    expect(banner.querySelector(".banner-hint")).toHaveTextContent(ENGINE_HINT);
    expect(within(banner).queryByText("The configuration file has errors")).toBeNull();
  });

  it("waits for the engine without asking anything", () => {
    const fetch = mockFetch({});
    renderI18n(<DatabasesPage engine={null} engineError="The engine did not start" />);
    expect(screen.getByRole("alert")).toHaveTextContent(`The engine did not start${ENGINE_HINT}`);
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(fetch).not.toHaveBeenCalled();
  });
});
