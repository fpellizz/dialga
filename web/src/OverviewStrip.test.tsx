import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { OverviewStrip } from "./OverviewStrip";
import { overview, renderI18n } from "./test/fixtures";
import { deferred, ENGINE, json, mockFetch, requestsTo, text } from "./test/http";
import { installIntersectionObserver } from "./test/intersection";

const OVERVIEW = "GET /api/v1/targets/dev-sql/overview";

function strip(container: HTMLElement) {
  return container.querySelector(".overview") as HTMLElement;
}

describe("OverviewStrip", () => {
  it("asks for the overview only once the card is on screen", async () => {
    const screenView = installIntersectionObserver();
    const fetch = mockFetch({ [OVERVIEW]: () => json(overview("dev-sql")) });
    const { container } = renderI18n(<OverviewStrip engine={ENGINE} target="dev-sql" />);

    expect(container.querySelectorAll(".skeleton")).toHaveLength(2);
    // Busy from the first render, before any request.
    expect(strip(container)).toHaveAttribute("aria-busy", "true");
    expect(screenView.observed()).toBe(1);
    expect(fetch).not.toHaveBeenCalled();

    act(() => screenView.show());
    expect(await screen.findByText("Available")).toBeInTheDocument();
    expect(fetch).toHaveBeenCalledTimes(1);
    // Seen once is enough: no observer left behind.
    expect(screenView.observed()).toBe(0);
    expect(requestsTo(fetch, OVERVIEW)[0].headers).toEqual({
      Authorization: `Bearer ${ENGINE.token}`,
    });
  });

  it("loads at once where IntersectionObserver is missing", async () => {
    const fetch = mockFetch({ [OVERVIEW]: () => json(overview("dev-sql")) });
    renderI18n(<OverviewStrip engine={ENGINE} target="dev-sql" />);
    expect(await screen.findByText("Available")).toBeInTheDocument();
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it("shows the instance, its storage and the SQL counts", async () => {
    mockFetch({
      [OVERVIEW]: () => json(overview("dev-sql", { sql: "ok", userDatabases: 12, activeTasks: 1 })),
    });
    const { container } = renderI18n(<OverviewStrip engine={ENGINE} target="dev-sql" />);

    expect(await screen.findByText("Available")).toHaveClass("badge-ok");
    expect(screen.getByText("SQL Server 2017 Standard")).toHaveAttribute(
      "title",
      "sqlserver-se 14.00.3465.1.v1",
    );
    expect(screen.getByText("db.m6i.large")).toBeInTheDocument();
    expect(screen.getByText("97.3 GiB free of 200 GiB (49%)")).toBeInTheDocument();
    expect(screen.getByRole("meter", { name: "Storage used" })).toHaveAttribute("value", "51");
    expect(container.querySelector(".meter-bar")).toHaveClass("meter-ok");
    expect(screen.getByText("Autoscaling up to 500 GiB")).toBeInTheDocument();

    const databases = screen.getByRole("link", { name: "12 · Show the databases of dev-sql" });
    expect(databases).toHaveAttribute("href", "#databases/dev-sql");
    expect(screen.getByText("Active tasks").nextElementSibling).toHaveTextContent("1");
    expect(screen.getByText("Active tasks").nextElementSibling).toHaveClass("tone-info");
    expect(within(strip(container)).queryByText(/SQL password/)).toBeNull();
  });

  it("warns when the storage runs low", async () => {
    mockFetch({ [OVERVIEW]: () => json(overview("dev-sql", { freeBytes: 10 * 1024 ** 3 })) });
    const { container } = renderI18n(<OverviewStrip engine={ENGINE} target="dev-sql" />);
    expect(await screen.findByText("10 GiB free of 200 GiB (5%)")).toBeInTheDocument();
    expect(container.querySelector(".meter-bar")).toHaveClass("meter-critical");
  });

  it("says why the free space is missing", async () => {
    const freeError = "free space not read from CloudWatch: AccessDenied";
    mockFetch({
      [OVERVIEW]: () =>
        json(overview("dev-sql", { freeBytes: undefined, freeError, maxAllocatedGiB: undefined })),
    });
    renderI18n(<OverviewStrip engine={ENGINE} target="dev-sql" />);
    expect(await screen.findByText("Free space not available from CloudWatch")).toHaveAttribute(
      "title",
      freeError,
    );
    expect(screen.queryByRole("meter")).toBeNull();
    expect(screen.queryByText(/Autoscaling/)).toBeNull();
  });

  it("explains a skipped SQL part", async () => {
    mockFetch({ [OVERVIEW]: () => json(overview("dev-sql")) });
    renderI18n(<OverviewStrip engine={ENGINE} target="dev-sql" />);
    const summary = await screen.findByText("Databases and tasks need the SQL password");
    expect(summary).not.toHaveClass("tone-warning");
    expect(
      screen.getByText("SQL password: sql.password is prompt: set DIALGA_SQL_PASSWORD"),
    ).toBeInTheDocument();
    expect(screen.queryByText("User databases")).toBeNull();
  });

  it("flags a failed SQL part", async () => {
    mockFetch({
      [OVERVIEW]: () =>
        json(overview("dev-sql", { sql: "failed", sqlDetail: "login failed for user 'admin'" })),
    });
    renderI18n(<OverviewStrip engine={ENGINE} target="dev-sql" />);
    expect(await screen.findByText("Databases and tasks not read")).toHaveClass("tone-warning");
    expect(screen.getByText("login failed for user 'admin'")).toBeInTheDocument();
    expect(
      screen.getByText("Check the SQL user of the target and the password the engine received."),
    ).toHaveClass("overview-hint");
  });

  it("shows a status it does not know as the engine sent it", async () => {
    mockFetch({ [OVERVIEW]: () => json(overview("dev-sql", { status: "upgrading" })) });
    renderI18n(<OverviewStrip engine={ENGINE} target="dev-sql" />);
    expect(await screen.findByText("upgrading")).toHaveClass("badge-running");
  });

  it("shows an AWS failure and retries on demand", async () => {
    const fetch = mockFetch({
      [OVERVIEW]: (_req, call) =>
        call === 0
          ? text("failed to get shared config profile, dev\n", 502)
          : json(overview("dev-sql")),
    });
    const { container } = renderI18n(<OverviewStrip engine={ENGINE} target="dev-sql" />);

    expect(await screen.findByText("failed to get shared config profile, dev")).toHaveClass(
      "tone-destructive",
    );
    expect(
      screen.getByText(
        "Create the profile dev in ~/.aws/config (aws configure sso), or fix aws_profile in the configuration.",
      ),
    ).toHaveClass("overview-hint");
    expect(strip(container)).toHaveAttribute("aria-busy", "false");
    expect(container.querySelector(".skeleton")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("Available")).toBeInTheDocument();
    expect(screen.queryByText(/failed to get shared config profile/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it("refreshes on demand and is busy meanwhile", async () => {
    const second = deferred<Response>();
    const fetch = mockFetch({
      [OVERVIEW]: (_req, call) => (call === 0 ? json(overview("dev-sql")) : second.promise),
    });
    const { container } = renderI18n(<OverviewStrip engine={ENGINE} target="dev-sql" />);
    await screen.findByText("Available");
    expect(strip(container)).toHaveAttribute("aria-busy", "false");

    const refresh = screen.getByRole("button", { name: "Refresh the overview of dev-sql" });
    await userEvent.click(refresh);
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(strip(container)).toHaveAttribute("aria-busy", "true");
    // Busy, not disabled: it keeps the focus, and a second click sends nothing.
    expect(refresh).toHaveAttribute("aria-disabled", "true");
    expect(refresh).toHaveFocus();
    await userEvent.click(refresh);
    expect(fetch).toHaveBeenCalledTimes(2);
    // The previous overview stays on screen while the new one loads.
    expect(screen.getByText("Available")).toBeInTheDocument();

    second.resolve(json(overview("dev-sql", { status: "backing-up" })));
    expect(await screen.findByText("Backing up")).toBeInTheDocument();
    expect(strip(container)).toHaveAttribute("aria-busy", "false");
  });

  it("aborts the request when the card goes away", async () => {
    const fetch = mockFetch({ [OVERVIEW]: () => new Promise<Response>(() => {}) });
    const { unmount } = renderI18n(<OverviewStrip engine={ENGINE} target="dev-sql" />);
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
    unmount();
    expect(requestsTo(fetch, OVERVIEW)[0].signal?.aborted).toBe(true);
  });
});
