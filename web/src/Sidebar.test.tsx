import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { type Page, Sidebar } from "./Sidebar";
import { renderI18n } from "./test/fixtures";
import { ENGINE } from "./test/http";

describe("Sidebar", () => {
  it.each([
    ["instances", "Instances", "Settings"],
    ["settings", "Settings", "Instances"],
  ] as [Page, string, string][])("marks %s as the current page", (page, current, other) => {
    renderI18n(<Sidebar page={page} engine={ENGINE} engineError="" />);
    expect(screen.getByRole("link", { name: current })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: other })).not.toHaveAttribute("aria-current");
  });

  it("links the pages by hash", () => {
    renderI18n(<Sidebar page="instances" engine={ENGINE} engineError="" />);
    expect(screen.getByRole("link", { name: "Instances" })).toHaveAttribute("href", "#instances");
    expect(screen.getByRole("link", { name: "Settings" })).toHaveAttribute("href", "#settings");
  });

  it("lists the coming pages as disabled, not as links", () => {
    renderI18n(<Sidebar page="instances" engine={ENGINE} engineError="" />);
    const nav = screen.getByRole("navigation", { name: "Main navigation" });
    expect(
      within(nav)
        .getAllByRole("link")
        .map((link) => link.textContent),
    ).toEqual(["Instances"]);
    for (const name of ["Import", "Export", "Activity"]) {
      const item = within(nav).getByText(name);
      expect(item).toHaveAttribute("aria-disabled", "true");
      expect(item).toHaveTextContent(`${name}Soon`);
    }
  });

  it.each([
    [null, "", "Starting engine…", "dot dot-wait"],
    [ENGINE, "", "engine 0.1.0-test", "dot"],
    [null, "spawn failed", "Engine not available", "dot dot-off"],
  ])("shows the engine state (%#)", (engine, engineError, status, dot) => {
    const { container } = renderI18n(
      <Sidebar page="instances" engine={engine} engineError={engineError} />,
    );
    const state = screen.getByText(status);
    expect(container.querySelector(".engine > span")).toHaveAttribute("class", dot);
    if (engineError) {
      expect(state).toHaveAttribute("title", engineError);
    } else {
      expect(state).not.toHaveAttribute("title");
    }
  });

  it("speaks Italian", () => {
    renderI18n(<Sidebar page="settings" engine={null} engineError="" />, "it");
    expect(screen.getByRole("navigation", { name: "Navigazione principale" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Istanze" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Impostazioni" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(screen.getAllByText("Presto")).toHaveLength(3);
    expect(screen.getByText("Avvio dell'engine…")).toBeInTheDocument();
  });
});
