import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { TargetList } from "./api";
import { InstancesPage } from "./InstancesPage";
import { CONFIG_PATH, DEV, LAB, PROD, renderI18n } from "./test/fixtures";
import { ENGINE, json, mockFetch, requestsTo, sseStream, text } from "./test/http";

const TARGETS = "GET /api/v1/targets";
const CHECK = "POST /api/v1/targets/dev-sql/checks";
const TASKS = "GET /api/v1/targets/dev-sql/tasks";

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
    expect(screen.getByRole("button", { name: "Refresh" })).toBeDisabled();
    expect(fetch).not.toHaveBeenCalled();
  });

  it("shows why the engine is not available", () => {
    const { container } = renderI18n(
      <InstancesPage engine={null} engineError="The engine did not start" />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("The engine did not start");
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
    expect(screen.queryByText("No instances yet")).toBeNull();
  });

  it("shows a failed request without the Error: prefix", async () => {
    mockFetch({ [TARGETS]: () => text("unauthorized\n", 401) });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
    expect(await screen.findByRole("alert")).toHaveTextContent(/^unauthorized$/);
  });

  it("shows one card per target with its facts", async () => {
    mockFetch({ [TARGETS]: list({ targets: [DEV, LAB, PROD] }) });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);

    const dev = await screen.findByRole("region", { name: "dev-sql" });
    expect(within(dev).getByText("my-sqlserver · eu-west-1")).toBeInTheDocument();
    expect(fact(dev, "Account")).toHaveTextContent("123456789012");
    expect(fact(dev, "Profile")).toHaveTextContent("default");
    expect(fact(dev, "Bucket")).toHaveTextContent("my-sqlserver-backups");
    expect(fact(dev, "Connection")).toHaveTextContent("Open tunnel on port 11433");
    expect(within(dev).queryByText("Protected")).toBeNull();

    expect(fact(card("lab-sql"), "Connection")).toHaveTextContent("Integrated tunnel");
    const prod = card("prod-sql");
    expect(fact(prod, "Connection")).toHaveTextContent("Direct to the endpoint");
    expect(within(prod).getByText("Protected")).toHaveClass("badge-warning");
  });

  it("reloads the list on Refresh", async () => {
    const fetch = mockFetch({
      [TARGETS]: (_req, call) =>
        json({ configPath: CONFIG_PATH, targets: call === 0 ? [DEV] : [DEV, PROD] }),
    });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
    await screen.findByRole("region", { name: "dev-sql" });

    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByRole("region", { name: "prod-sql" })).toBeInTheDocument();
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it("streams a check: notice, steps and a verdict with warnings", async () => {
    const stream = sseStream();
    const fetch = mockFetch({
      [TARGETS]: list({ targets: [DEV] }),
      [CHECK]: () => stream.response,
    });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
    const dev = await screen.findByRole("region", { name: "dev-sql" });

    await userEvent.click(within(dev).getByRole("button", { name: "Run check" }));
    const running = await within(dev).findByRole("button", { name: "Checking…" });
    expect(running).toBeDisabled();
    const [init] = requestsTo(fetch, CHECK);
    expect(init.headers).toEqual({ Authorization: `Bearer ${ENGINE.token}` });

    stream.send("notice", { message: "SQL login will be skipped: DIALGA_SQL_PASSWORD is empty" });
    expect(
      await within(dev).findByText("SQL login will be skipped: DIALGA_SQL_PASSWORD is empty"),
    ).toBeInTheDocument();

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

    expect(await within(dev).findByText("All checks passed, with warnings")).toHaveClass(
      "banner-ok",
    );
    // A step that ends replaces the row it started, it does not add one.
    expect(within(dev).getAllByRole("listitem")).toHaveLength(2);
    expect(within(aws).getByText("Passed")).toBeInTheDocument();
    expect(within(dev).getByRole("button", { name: "Run check" })).toBeEnabled();
  });

  it.each([
    [true, "ok", "All checks passed", "banner-ok"],
    [false, "failed", "Some checks failed", "banner-fail"],
  ] as const)("shows the verdict ok=%s", async (ok, status, verdict, tone) => {
    const stream = sseStream();
    mockFetch({ [TARGETS]: list({ targets: [DEV] }), [CHECK]: () => stream.response });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
    const dev = await screen.findByRole("region", { name: "dev-sql" });
    await userEvent.click(within(dev).getByRole("button", { name: "Run check" }));

    stream.send("step", { step: "aws", label: "AWS credentials", status });
    stream.send("done", { ok });
    stream.close();
    expect(await within(dev).findByText(verdict)).toHaveClass(tone);
  });

  it("shows a check refused by the engine", async () => {
    mockFetch({
      [TARGETS]: list({ targets: [DEV] }),
      [CHECK]: () => text("connect.mode tunnel arrives in v0.4\n", 422),
    });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
    const dev = await screen.findByRole("region", { name: "dev-sql" });
    await userEvent.click(within(dev).getByRole("button", { name: "Run check" }));

    expect(await within(dev).findByRole("alert")).toHaveTextContent(
      /^connect\.mode tunnel arrives in v0\.4$/,
    );
    expect(within(dev).getByRole("button", { name: "Run check" })).toBeEnabled();
    expect(within(dev).queryByText("Some checks failed")).toBeNull();
  });

  it("starts every check from a clean slate", async () => {
    const first = sseStream();
    const second = sseStream();
    mockFetch({
      [TARGETS]: list({ targets: [DEV] }),
      [CHECK]: (_req, call) => (call === 0 ? first.response : second.response),
    });
    renderI18n(<InstancesPage engine={ENGINE} engineError="" />);
    const dev = await screen.findByRole("region", { name: "dev-sql" });

    await userEvent.click(within(dev).getByRole("button", { name: "Run check" }));
    first.send("notice", { message: "first notice" });
    first.send("step", { step: "aws", label: "AWS credentials", status: "failed" });
    first.send("done", { ok: false });
    first.close();
    await within(dev).findByText("Some checks failed");

    await userEvent.click(within(dev).getByRole("button", { name: "Run check" }));
    await within(dev).findByRole("button", { name: "Checking…" });
    expect(within(dev).queryByText("first notice")).toBeNull();
    expect(within(dev).queryByText("Some checks failed")).toBeNull();
    expect(within(dev).queryByRole("listitem")).toBeNull();
    second.close();
  });

  it("shows and hides the tasks of a target", async () => {
    const fetch = mockFetch({
      [TARGETS]: list({ targets: [DEV] }),
      [TASKS]: () => json({ tasks: [] }),
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
});
