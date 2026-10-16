import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { TargetList } from "./api";
import { InstancesPage } from "./InstancesPage";
import { CONFIG_PATH, DEV, discovered, LAB, overview, PROD, renderI18n } from "./test/fixtures";
import { ENGINE, json, mockFetch, requestsTo, sseStream, text } from "./test/http";

const TARGETS = "GET /api/v1/targets";
const CHECK = "POST /api/v1/targets/dev-sql/checks";
const TASKS = "GET /api/v1/targets/dev-sql/tasks";

/** Every card loads its overview strip (OverviewStrip.test.tsx covers it). */
const OVERVIEWS = Object.fromEntries(
  [DEV, LAB, PROD].map((t) => [
    `GET /api/v1/targets/${t.name}/overview`,
    () => json(overview(t.name)),
  ]),
);

/** The tunnel-mode card follows its shared tunnel: one state, then the stream stays open. */
const TUNNEL = {
  "GET /api/v1/targets/lab-sql/tunnel/events": () => {
    const events = sseStream();
    events.send("state", { target: "lab-sql", status: "closed", users: 0, keepOpen: false });
    return events.response;
  },
};

/** The page Refresh; its name says what it refreshes. */
function pageRefresh() {
  return within(screen.getByRole("banner")).getByRole("button", { name: "Refresh the instances" });
}

/** The live region of a card: progress and outcome of its check. */
function announced(region: HTMLElement) {
  return within(region).getByRole("status");
}

const ENGINE_HINT =
  "Restart Dialga. If it happens again, start it from a terminal to read the engine log.";

function list(body: Partial<TargetList>) {
  return () => json({ configPath: CONFIG_PATH, targets: [], ...body });
}

function card(name: string) {
  return screen.getByRole("region", { name });
}

/** The value next to a label in a card's facts. */
function fact(region: HTMLElement, label: string) {
  return within(region).getByText(label).nextElementSibling;
}

describe("InstancesPage", () => {
  it("shows skeletons while the engine starts", () => {
    const fetch = mockFetch({});
    const { container } = renderI18n(<InstancesPage engine={null} engineError="" />);
    expect(container.querySelectorAll(".card.skeleton")).toHaveLength(2);
    expect(pageRefresh()).toBeDisabled();
    expect(fetch).not.toHaveBeenCalled();
  });

  it("shows why the engine is not available", () => {
    const { container } = renderI18n(
      <InstancesPage engine={null} engineError="The engine did not start" />,
    );
    const banner = screen.getByRole("alert");
    expect(banner.querySelector(".banner-text")).toHaveTextContent("The engine did not start");
    expect(banner.querySelector(".banner-hint")).toHaveTextContent(ENGINE_HINT);
    expect(container.querySelector(".skeleton")).toBeNull();
  });

  it("shows the empty state with the path of the configuration", async () => {
    mockFetch({ [TARGETS]: list({}) });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
    const empty = await screen.findByRole("status");
    expect(within(empty).getByRole("heading", { name: "No instances yet" })).toBeInTheDocument();
    expect(within(empty).getByText(CONFIG_PATH)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows the errors of the configuration instead of the empty state", async () => {
    const error = `${CONFIG_PATH}: target "dev-sql": field bogus not found`;
    mockFetch({ [TARGETS]: list({ error }) });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
    const banner = await screen.findByRole("alert");
    expect(within(banner).getByText("The configuration file has errors")).toBeInTheDocument();
    expect(banner.querySelector("pre")).toHaveTextContent(error);
    expect(banner).toHaveTextContent(
      "Fix the configuration file (its path is in Settings), then refresh.",
    );
    expect(screen.queryByText("No instances yet")).toBeNull();
  });

  it("shows a failed request without the Error: prefix", async () => {
    mockFetch({ [TARGETS]: () => text("unauthorized\n", 401) });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
    const banner = await screen.findByRole("alert");
    expect(banner.querySelector(".banner-text")).toHaveTextContent(/^unauthorized$/);
    // 401: the session token is not the engine's.
    expect(banner.querySelector(".banner-hint")).toHaveTextContent(ENGINE_HINT);
  });

  it("shows one card per target with its facts", async () => {
    mockFetch({ [TARGETS]: list({ targets: [DEV, LAB, PROD] }), ...OVERVIEWS, ...TUNNEL });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);

    const dev = await screen.findByRole("region", { name: "dev-sql" });
    expect(within(dev).getByText("my-sqlserver · eu-west-1")).toBeInTheDocument();
    expect(fact(dev, "Account")).toHaveTextContent("123456789012");
    expect(fact(dev, "Profile")).toHaveTextContent("default");
    expect(fact(dev, "Bucket")).toHaveTextContent("my-sqlserver-backups");
    expect(fact(dev, "Connection")).toHaveTextContent("Open tunnel on port 11433");
    expect(within(dev).queryByText("Protected")).toBeNull();
    // The overview strip of the card, from GET /overview.
    expect(await within(dev).findByText("SQL Server 2017 Standard")).toBeInTheDocument();

    const lab = card("lab-sql");
    expect(fact(lab, "Connection")).toHaveTextContent("Integrated tunnel");
    // Only a tunnel-mode target shows its shared tunnel.
    expect(await within(lab).findByText("Not connected")).toBeInTheDocument();
    expect(within(lab).getByRole("button", { name: "Connect" })).toBeEnabled();
    expect(within(dev).queryByRole("button", { name: "Connect" })).toBeNull();
    const prod = card("prod-sql");
    expect(fact(prod, "Connection")).toHaveTextContent("Direct to the endpoint");
    expect(within(prod).getByText("Protected")).toHaveClass("badge-warning");
  });

  it("reloads the list on Refresh", async () => {
    const fetch = mockFetch({
      [TARGETS]: (_req, call) =>
        json({ configPath: CONFIG_PATH, targets: call === 0 ? [DEV] : [DEV, PROD] }),
      ...OVERVIEWS,
    });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
    const dev = await screen.findByRole("region", { name: "dev-sql" });
    expect(await within(dev).findByText("Available")).toBeInTheDocument();

    await userEvent.click(pageRefresh());
    expect(await screen.findByRole("region", { name: "prod-sql" })).toBeInTheDocument();
    expect(requestsTo(fetch, TARGETS)).toHaveLength(2);
    // The card that stays keeps its strip; only the new one asks for its overview.
    await waitFor(() =>
      expect(requestsTo(fetch, "GET /api/v1/targets/prod-sql/overview")).toHaveLength(1),
    );
    expect(requestsTo(fetch, "GET /api/v1/targets/dev-sql/overview")).toHaveLength(1);
  });

  it("streams a check: notice, steps and a verdict with warnings", async () => {
    const stream = sseStream();
    const fetch = mockFetch({
      [TARGETS]: list({ targets: [DEV] }),
      [CHECK]: () => stream.response,
      ...OVERVIEWS,
    });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
    const dev = await screen.findByRole("region", { name: "dev-sql" });

    await userEvent.click(within(dev).getByRole("button", { name: "Run check" }));
    const running = await within(dev).findByRole("button", { name: "Checking…" });
    // Busy, not disabled: it keeps the focus, and another click starts nothing.
    expect(running).toHaveAttribute("aria-disabled", "true");
    expect(running).toHaveFocus();
    await userEvent.click(running);
    expect(requestsTo(fetch, CHECK)).toHaveLength(1);
    const [init] = requestsTo(fetch, CHECK);
    expect(init.headers).toEqual({ Authorization: `Bearer ${ENGINE.token}` });
    expect(announced(dev)).toHaveTextContent(/^Checking…$/);

    stream.send("notice", { message: "SQL login will be skipped: DIALGA_SQL_PASSWORD is empty" });
    expect(
      await within(dev).findByText("SQL login will be skipped: DIALGA_SQL_PASSWORD is empty"),
    ).toHaveClass("banner-text");
    // One polite region per card: the progress with the notice, not every step.
    expect(announced(dev)).toHaveTextContent(
      /^Checking… SQL login will be skipped: DIALGA_SQL_PASSWORD is empty$/,
    );

    stream.send("step", { step: "aws", label: "AWS credentials", status: "running" });
    const aws = (await within(dev).findByText("AWS credentials")).closest("li") as HTMLElement;
    expect(within(aws).getByText("Running")).toBeInTheDocument();

    stream.send("step", { step: "aws", label: "AWS credentials", status: "ok", detail: "ok" });
    stream.send("step", {
      step: "storage",
      label: "Storage",
      status: "warning",
      detail: "no CloudWatch data",
    });
    stream.send("done", { ok: true });
    stream.close();

    expect(
      await within(dev).findByText("All checks passed, with warnings", { selector: ".banner" }),
    ).toHaveClass("banner-ok");
    expect(announced(dev)).toHaveTextContent(/^All checks passed, with warnings$/);
    // A step that ends replaces the row it started, it does not add one.
    expect(within(dev).getAllByRole("listitem")).toHaveLength(2);
    expect(within(aws).getByText("Passed")).toBeInTheDocument();
    expect(within(dev).getByRole("button", { name: "Run check" })).not.toHaveAttribute(
      "aria-disabled",
    );
  });

  it.each([
    [true, "ok", "All checks passed", "banner-ok"],
    [false, "failed", "Some checks failed · Failed: 1 · warnings: 0", "banner-fail"],
  ] as const)("shows the verdict ok=%s", async (ok, status, verdict, tone) => {
    const stream = sseStream();
    mockFetch({
      [TARGETS]: list({ targets: [DEV] }),
      [CHECK]: () => stream.response,
      ...OVERVIEWS,
    });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
    const dev = await screen.findByRole("region", { name: "dev-sql" });
    await userEvent.click(within(dev).getByRole("button", { name: "Run check" }));

    stream.send("step", { step: "aws", label: "AWS credentials", status });
    stream.send("done", { ok });
    stream.close();
    expect(await within(dev).findByText(verdict, { selector: ".banner" })).toHaveClass(tone);
    expect(announced(dev)).toHaveTextContent(verdict);
  });

  it("shows a check refused by the engine", async () => {
    mockFetch({
      [TARGETS]: list({ targets: [DEV] }),
      [CHECK]: () => text("connect.mode tunnel arrives in v0.4\n", 422),
      ...OVERVIEWS,
    });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
    const dev = await screen.findByRole("region", { name: "dev-sql" });
    await userEvent.click(within(dev).getByRole("button", { name: "Run check" }));

    expect(await within(dev).findByRole("alert")).toHaveTextContent(
      /^connect\.mode tunnel arrives in v0\.4$/,
    );
    expect(within(dev).getByRole("button", { name: "Run check" })).not.toHaveAttribute(
      "aria-disabled",
    );
    expect(within(dev).queryByText(/Some checks failed/)).toBeNull();
  });

  it("starts every check from a clean slate", async () => {
    const first = sseStream();
    const second = sseStream();
    mockFetch({
      [TARGETS]: list({ targets: [DEV] }),
      [CHECK]: (_req, call) => (call === 0 ? first.response : second.response),
      ...OVERVIEWS,
    });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
    const dev = await screen.findByRole("region", { name: "dev-sql" });

    await userEvent.click(within(dev).getByRole("button", { name: "Run check" }));
    first.send("notice", { message: "first notice" });
    first.send("step", { step: "aws", label: "AWS credentials", status: "failed" });
    first.send("done", { ok: false });
    first.close();
    await within(dev).findByText(/^Some checks failed/, { selector: ".banner" });

    await userEvent.click(within(dev).getByRole("button", { name: "Run check" }));
    await within(dev).findByRole("button", { name: "Checking…" });
    expect(within(dev).queryByText("first notice")).toBeNull();
    expect(within(dev).queryByText(/Some checks failed/)).toBeNull();
    expect(announced(dev)).toHaveTextContent(/^Checking…$/);
    expect(within(dev).queryByRole("listitem")).toBeNull();
    second.close();
  });

  it("shows and hides the tasks of a target", async () => {
    const fetch = mockFetch({
      [TARGETS]: list({ targets: [DEV] }),
      [TASKS]: () => json({ tasks: [] }),
      ...OVERVIEWS,
    });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
    const dev = await screen.findByRole("region", { name: "dev-sql" });
    const toggle = within(dev).getByRole("button", { name: "Show tasks" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(requestsTo(fetch, TASKS)).toHaveLength(0);

    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(toggle).toHaveAccessibleName("Hide tasks");
    expect(
      await within(dev).findByText("No native tasks in the last 36 days."),
    ).toBeInTheDocument();
    expect(document.getElementById(toggle.getAttribute("aria-controls") ?? "")).toContainElement(
      within(dev).getByRole("region", { name: "Native backup and restore tasks" }),
    );

    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    await waitFor(() =>
      expect(within(dev).queryByText("No native tasks in the last 36 days.")).toBeNull(),
    );
  });

  describe("add instance", () => {
    /** The first run: no targets, then the one the dialog saves. */
    function firstRun() {
      return mockFetch({
        [TARGETS]: (_req, call) =>
          json({ configPath: CONFIG_PATH, targets: call === 0 ? [] : [DEV] }),
        "GET /api/v1/discovery/profiles": () => json({ profiles: [{ name: "default" }] }),
        "GET /api/v1/discovery/instances": () =>
          json({
            profile: "default",
            region: "us-east-1",
            accountId: DEV.accountId,
            instances: [discovered("dev-sql")],
          }),
        "POST /api/v1/config/targets": () => json(DEV, 201),
        ...OVERVIEWS,
      });
    }

    it("saves the first target from the empty state and shows its card", async () => {
      const fetch = firstRun();
      renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
      const empty = await screen.findByRole("status");
      // The header offers Add only once there is a list to add to.
      expect(
        within(screen.getByRole("banner")).queryByRole("button", { name: "Add instance" }),
      ).toBeNull();

      await userEvent.click(within(empty).getByRole("button", { name: "Add instance" }));
      const dialog = screen.getByRole("dialog", { name: "Add instance" });
      await within(dialog).findByRole("combobox", { name: "AWS profile" });
      await userEvent.click(within(dialog).getByRole("button", { name: "Discover instances" }));
      await userEvent.type(
        await within(dialog).findByRole("textbox", { name: "S3 bucket" }),
        DEV.bucket,
      );
      await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));

      const confirmation = await screen.findByText("Target dev-sql saved. Run check to verify it.");
      expect(confirmation).toHaveFocus();
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(await screen.findByRole("region", { name: "dev-sql" })).toBeInTheDocument();
      expect(requestsTo(fetch, TARGETS)).toHaveLength(2);
      expect(screen.queryByText("No instances yet")).toBeNull();
      expect(
        within(screen.getByRole("banner")).getByRole("button", { name: "Add instance" }),
      ).toBeEnabled();
    });

    it("closes the dialog without saving", async () => {
      const fetch = firstRun();
      renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
      const add = within(await screen.findByRole("status")).getByRole("button", {
        name: "Add instance",
      });
      await userEvent.click(add);
      const dialog = screen.getByRole("dialog", { name: "Add instance" });
      await within(dialog).findByRole("combobox", { name: "AWS profile" });

      await userEvent.click(within(dialog).getAllByRole("button", { name: "Cancel" })[1]);
      expect(screen.queryByRole("dialog")).toBeNull();
      // The focus goes back to the button that opened the dialog.
      expect(add).toHaveFocus();
      expect(requestsTo(fetch, TARGETS)).toHaveLength(1);
      expect(requestsTo(fetch, "POST /api/v1/config/targets")).toHaveLength(0);
    });
  });
});
