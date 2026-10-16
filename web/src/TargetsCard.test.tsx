import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { TargetsCard } from "./TargetsCard";
import { CONFIG_PATH, DEV, PROD, renderI18n } from "./test/fixtures";
import { deferred, ENGINE, json, mockFetch, text } from "./test/http";

const TARGETS = "GET /api/v1/targets";
const REMOVE_DEV = "DELETE /api/v1/config/targets/dev-sql";

function card() {
  return screen.getByRole("region", { name: "Targets" });
}

function confirmField() {
  return screen.getByRole("textbox", { name: "Target name" });
}

function confirmButton() {
  return screen.getByRole("button", { name: "Remove target" });
}

/** The DELETE requests, as the URL they were sent to. */
function removals(fetch: ReturnType<typeof mockFetch>) {
  return fetch.mock.calls
    .filter(([, init]) => init?.method === "DELETE")
    .map(([url]) => String(url));
}

describe("TargetsCard", () => {
  it("lists the targets with their instance, region and profile", async () => {
    mockFetch({ [TARGETS]: () => json({ configPath: CONFIG_PATH, targets: [DEV, PROD] }) });
    const { container } = renderI18n(<TargetsCard engine={ENGINE} />);
    expect(container.querySelector(".skeleton")).not.toBeNull();

    const [dev, prod] = await within(card()).findAllByRole("listitem");
    expect(within(dev).getByText("dev-sql")).toBeInTheDocument();
    expect(within(dev).getByText("my-sqlserver · eu-west-1 · default")).toBeInTheDocument();
    expect(within(dev).queryByText("Protected")).toBeNull();
    expect(within(prod).getByText("Protected")).toHaveClass("badge-warning");
    expect(screen.getByRole("button", { name: "Remove dev-sql" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Remove prod-sql" })).toBeEnabled();
  });

  it("says when there is no target yet", async () => {
    mockFetch({ [TARGETS]: () => json({ configPath: CONFIG_PATH, targets: [] }) });
    renderI18n(<TargetsCard engine={ENGINE} />);
    expect(
      await screen.findByText("No targets yet. Add one from the Instances page."),
    ).toBeInTheDocument();
  });

  it.each([
    [
      "the errors of the configuration",
      () => json({ configPath: CONFIG_PATH, error: "invalid YAML: line 2", targets: [] }),
      "invalid YAML: line 2",
    ],
    ["a failed request", () => text("unauthorized\n", 401), "unauthorized"],
  ])("shows %s", async (_name, answer, message) => {
    mockFetch({ [TARGETS]: answer });
    renderI18n(<TargetsCard engine={ENGINE} />);
    const banner = await within(card()).findByRole("alert");
    expect(banner.querySelector("pre, .banner-text")).toHaveTextContent(new RegExp(`^${message}$`));
    expect(screen.queryByText(/No targets yet/)).toBeNull();
  });

  it("removes a target only after its name is typed again", async () => {
    const fetch = mockFetch({
      [TARGETS]: (_req, call) =>
        json({ configPath: CONFIG_PATH, targets: call === 0 ? [DEV, PROD] : [PROD] }),
      [REMOVE_DEV]: () => new Response(null, { status: 204 }),
    });
    renderI18n(<TargetsCard engine={ENGINE} />);
    const remove = await screen.findByRole("button", { name: "Remove dev-sql" });

    await userEvent.click(remove);
    const form = screen.getByRole("form", { name: "Remove dev-sql?" });
    expect(remove).toBeDisabled();
    expect(confirmField()).toHaveFocus();
    expect(confirmField()).toHaveAccessibleDescription(
      "The target leaves the configuration file; nothing changes on AWS. Type dev-sql to confirm.",
    );
    expect(confirmButton()).toBeDisabled();

    // Enter with a wrong name sends nothing.
    await userEvent.type(confirmField(), "dev-sq{Enter}");
    expect(confirmButton()).toBeDisabled();
    expect(removals(fetch)).toEqual([]);
    await userEvent.type(confirmField(), "L");
    expect(confirmButton()).toBeDisabled();

    await userEvent.clear(confirmField());
    await userEvent.type(confirmField(), "dev-sql");
    expect(confirmButton()).toBeEnabled();
    await userEvent.click(confirmButton());

    expect(await within(card()).findByRole("status")).toHaveTextContent("Target dev-sql removed.");
    expect(removals(fetch)).toEqual([
      `${ENGINE.url}/api/v1/config/targets/dev-sql?confirm=dev-sql`,
    ]);
    expect(form).not.toBeInTheDocument();
    expect(screen.queryByText("dev-sql")).toBeNull();
    await waitFor(() => expect(screen.getByRole("heading", { name: "Targets" })).toHaveFocus());
  });

  it.each([
    ["Escape", () => userEvent.keyboard("{Escape}")],
    ["Cancel", () => userEvent.click(screen.getByRole("button", { name: "Cancel" }))],
  ])("cancels with %s and gives the focus back", async (_name, cancel) => {
    mockFetch({ [TARGETS]: () => json({ configPath: CONFIG_PATH, targets: [DEV] }) });
    renderI18n(<TargetsCard engine={ENGINE} />);
    const remove = await screen.findByRole("button", { name: "Remove dev-sql" });
    await userEvent.click(remove);
    await userEvent.type(confirmField(), "dev");

    await cancel();
    expect(screen.queryByRole("form")).toBeNull();
    await waitFor(() => expect(remove).toHaveFocus());
    expect(remove).toBeEnabled();

    // A new attempt starts empty.
    await userEvent.click(remove);
    expect(confirmField()).toHaveValue("");
  });

  it("keeps the form open with the engine's refusal", async () => {
    const answer = deferred<Response>();
    mockFetch({
      [TARGETS]: () => json({ configPath: CONFIG_PATH, targets: [DEV] }),
      [REMOVE_DEV]: () => answer.promise,
    });
    renderI18n(<TargetsCard engine={ENGINE} />);
    await userEvent.click(await screen.findByRole("button", { name: "Remove dev-sql" }));
    await userEvent.type(confirmField(), "dev-sql");
    await userEvent.click(confirmButton());
    expect(confirmButton()).toHaveAttribute("aria-disabled", "true");

    answer.resolve(text("config: cannot write config.yaml: read-only file system\n", 500));
    const form = screen.getByRole("form", { name: "Remove dev-sql?" });
    const alert = await within(form).findByRole("alert");
    expect(alert.querySelector(".banner-text")).toHaveTextContent(
      /^config: cannot write config\.yaml: read-only file system$/,
    );
    expect(confirmButton()).not.toHaveAttribute("aria-disabled");
    expect(confirmButton()).toBeEnabled();
    // The live region of the confirmation is always there: nothing was removed.
    expect(within(card()).getByRole("status")).toBeEmptyDOMElement();
  });

  it("waits for the engine without asking anything", () => {
    const fetch = mockFetch({});
    const { container } = renderI18n(<TargetsCard engine={null} />);
    expect(container.querySelector(".skeleton")).not.toBeNull();
    expect(fetch).not.toHaveBeenCalled();
  });
});
