import { act, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { TunnelState } from "./api";
import { type Page, Sidebar } from "./Sidebar";
import { renderI18n } from "./test/fixtures";
import { ENGINE, json, mockFetch, type Route } from "./test/http";

const TUNNELS = "GET /api/v1/tunnels";

function tunnel(target: string, status: TunnelState["status"]): TunnelState {
  return { target, status, users: 0, keepOpen: false };
}

function tunnels(...list: TunnelState[]): Route {
  return () => json({ tunnels: list });
}

describe("Sidebar", () => {
  beforeEach(() => {
    // The tunnel summary asks the engine as soon as there is one.
    mockFetch({ [TUNNELS]: tunnels() });
  });

  it.each([
    ["instances", "Instances"],
    ["databases", "Databases"],
    ["settings", "Settings"],
    ["about", "About"],
  ] as [Page, string][])("marks %s as the current page", (page, current) => {
    renderI18n(<Sidebar page={page} engine={ENGINE} engineError="" />);
    for (const link of screen.getAllByRole("link")) {
      if (link.textContent === current) {
        expect(link).toHaveAttribute("aria-current", "page");
      } else {
        expect(link).not.toHaveAttribute("aria-current");
      }
    }
  });

  it("links the pages by hash", () => {
    renderI18n(<Sidebar page="instances" engine={ENGINE} engineError="" />);
    expect(screen.getByRole("link", { name: "Instances" })).toHaveAttribute("href", "#instances");
    expect(screen.getByRole("link", { name: "Databases" })).toHaveAttribute("href", "#databases");
    expect(screen.getByRole("link", { name: "Settings" })).toHaveAttribute("href", "#settings");
    expect(screen.getByRole("link", { name: "About" })).toHaveAttribute("href", "#about");
  });

  it("links every page", () => {
    renderI18n(<Sidebar page="instances" engine={ENGINE} engineError="" />);
    const nav = screen.getByRole("navigation", { name: "Main navigation" });
    expect(
      within(nav)
        .getAllByRole("link")
        .map((link) => link.textContent),
    ).toEqual(["Instances", "Databases", "Import", "Export", "Activity", "Settings", "About"]);
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

  it("offers a skip link to the page content", async () => {
    renderI18n(
      <>
        <Sidebar page="instances" engine={ENGINE} engineError="" />
        <main id="main" tabIndex={-1} />
      </>,
    );
    await userEvent.click(screen.getByRole("button", { name: "Skip to content" }));
    expect(screen.getByRole("main")).toHaveFocus();
  });

  it.each([
    ["instances", "Instances · Dialga"],
    ["databases", "Databases · Dialga"],
    ["settings", "Settings · Dialga"],
    ["about", "About · Dialga"],
  ] as [Page, string][])("titles the window after the %s page", (page, title) => {
    renderI18n(<Sidebar page={page} engine={null} engineError="" />);
    expect(document.title).toBe(title);
  });

  describe("tunnels", () => {
    it("says nothing while no tunnel is open", async () => {
      const fetch = mockFetch({ [TUNNELS]: tunnels(tunnel("lab-sql", "closed")) });
      const { container } = renderI18n(<Sidebar page="instances" engine={ENGINE} engineError="" />);
      await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
      expect(container.querySelectorAll(".engine")).toHaveLength(1);
    });

    it("counts the open tunnels and the ones on their way", async () => {
      mockFetch({
        [TUNNELS]: tunnels(
          tunnel("dev-sql", "ready"),
          tunnel("lab-sql", "ready"),
          tunnel("prod-sql", "connecting"),
        ),
      });
      renderI18n(<Sidebar page="instances" engine={ENGINE} engineError="" />);
      expect(await screen.findByText("2 tunnels open · 1 connecting")).toBeInTheDocument();
    });

    it("refreshes the summary every 30 seconds", async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const fetch = mockFetch({
        [TUNNELS]: (_req, call) =>
          json({ tunnels: call === 0 ? [] : [tunnel("dev-sql", "ready")] }),
      });
      renderI18n(<Sidebar page="instances" engine={ENGINE} engineError="" />);
      await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
      expect(screen.queryByText(/tunnels? open/)).toBeNull();

      await act(() => vi.advanceTimersByTimeAsync(30_000));
      expect(await screen.findByText("1 tunnel open")).toBeInTheDocument();
      expect(fetch).toHaveBeenCalledTimes(2);
    });

    it("asks nothing before the engine is up", () => {
      const fetch = mockFetch({});
      renderI18n(<Sidebar page="instances" engine={null} engineError="" />);
      expect(fetch).not.toHaveBeenCalled();
    });
  });

  it("speaks Italian", () => {
    renderI18n(<Sidebar page="settings" engine={null} engineError="" />, "it");
    expect(screen.getByRole("navigation", { name: "Navigazione principale" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Istanze" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Database" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Impostazioni" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(screen.getByRole("link", { name: "Importa" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Esporta" })).toBeInTheDocument();
    expect(screen.queryByText("Presto")).not.toBeInTheDocument();
    expect(screen.getByText("Avvio dell'engine…")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Vai al contenuto" })).toBeInTheDocument();
    expect(document.title).toBe("Impostazioni · Dialga");
  });
});
