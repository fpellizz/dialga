import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { PrerequisitesCard } from "./PrerequisitesCard";
import { PLUGIN_FOUND, RUNTIME_MISSING, renderI18n } from "./test/fixtures";
import { deferred, ENGINE, json, mockFetch, text } from "./test/http";

const PREREQS = "GET /api/v1/prerequisites";

function card() {
  return screen.getByRole("region", { name: "Prerequisites" });
}

function item(name: string) {
  return within(card()).getByText(name).closest("li") as HTMLElement;
}

describe("PrerequisitesCard", () => {
  it("shows a skeleton, then what was found and how to install the rest", async () => {
    const gate = deferred<void>();
    mockFetch({
      [PREREQS]: async () => {
        await gate.promise;
        return json({ prerequisites: [PLUGIN_FOUND, RUNTIME_MISSING] });
      },
    });
    const { container } = renderI18n(<PrerequisitesCard engine={ENGINE} />);
    expect(container.querySelectorAll(".skeleton")).toHaveLength(2);
    expect(card()).toHaveAttribute("aria-busy", "true");
    expect(screen.getByRole("button", { name: "Check again" })).toBeDisabled();

    gate.resolve();
    await screen.findByText("Session Manager plugin");
    const plugin = within(item("Session Manager plugin"));
    expect(card()).toHaveAttribute("aria-busy", "false");
    expect(plugin.getByText("Found")).toHaveClass("badge-ok");
    expect(
      plugin.getByText("session-manager-plugin 1.2.707.0 (/usr/local/bin/session-manager-plugin)"),
    ).toBeInTheDocument();
    expect(plugin.queryByText(/Install on/)).toBeNull();

    const runtime = within(item("Container runtime"));
    expect(runtime.getByText("Missing")).toHaveClass("badge-warning");
    expect(
      runtime.getByText("Needed to inspect .bak files: podman or docker."),
    ).toBeInTheDocument();
    expect(runtime.getByText("Install on Fedora").nextElementSibling).toHaveTextContent(
      "sudo dnf install podman",
    );
    expect(runtime.getByText("Install on Debian/Ubuntu").nextElementSibling).toHaveTextContent(
      "sudo apt install podman",
    );
    expect(screen.getByRole("button", { name: "Check again" })).toBeEnabled();
  });

  it("asks to update a plugin too old for the integrated tunnel", async () => {
    mockFetch({
      [PREREQS]: () =>
        json({
          prerequisites: [
            {
              ...PLUGIN_FOUND,
              version: "1.2.0.0",
              outdated: true,
              install: [{ distro: "fedora", command: "sudo dnf upgrade session-manager-plugin" }],
            },
          ],
        }),
    });
    renderI18n(<PrerequisitesCard engine={ENGINE} />);
    expect(await screen.findByText("Update needed")).toHaveClass("badge-warning");
    const plugin = within(item("Session Manager plugin"));
    expect(
      plugin.getByText(
        "Version 1.2.0.0 is too old for the integrated tunnel: install the latest one.",
      ),
    ).toBeInTheDocument();
    expect(plugin.getByText(/1\.2\.0\.0 \(/)).toBeInTheDocument();
    expect(plugin.getByText("sudo dnf upgrade session-manager-plugin")).toBeInTheDocument();
  });

  it("shows a failure without the Error: prefix and checks again on demand", async () => {
    const fetch = mockFetch({
      [PREREQS]: (_req, call) =>
        call === 0
          ? text("prerequisites: exec failed\n", 500)
          : json({ prerequisites: [PLUGIN_FOUND] }),
    });
    renderI18n(<PrerequisitesCard engine={ENGINE} />);
    expect(await within(card()).findByRole("alert")).toHaveTextContent(
      /^prerequisites: exec failed$/,
    );
    expect(card().querySelector(".skeleton")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Check again" }));
    expect(await screen.findByText("Found")).toBeInTheDocument();
    expect(within(card()).queryByRole("alert")).toBeNull();
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it("speaks Italian", async () => {
    mockFetch({ [PREREQS]: () => json({ prerequisites: [RUNTIME_MISSING] }) });
    renderI18n(<PrerequisitesCard engine={ENGINE} />, "it");
    expect(await screen.findByText("Mancante")).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Prerequisiti" })).toBeInTheDocument();
  });

  it("waits for the engine without asking anything", () => {
    const fetch = mockFetch({});
    renderI18n(<PrerequisitesCard engine={null} />);
    expect(screen.getByRole("button", { name: "Check again" })).toBeDisabled();
    expect(fetch).not.toHaveBeenCalled();
  });
});
