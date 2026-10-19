import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { CONFIG_PATH } from "./test/fixtures";
import { ENGINE, json, mockFetch } from "./test/http";
import { installMatchMedia } from "./test/media";

/** The browser dev server: engine URL and token from VITE_ENGINE_*. */
function devEngine() {
  vi.stubEnv("VITE_ENGINE_URL", ENGINE.url);
  vi.stubEnv("VITE_ENGINE_TOKEN", ENGINE.token);
  return mockFetch({
    "GET /api/v1/targets": () => json({ configPath: CONFIG_PATH, targets: [] }),
    "GET /api/v1/prerequisites": () => json({ prerequisites: [] }),
    // The tunnel summary of the sidebar.
    "GET /api/v1/tunnels": () => json({ tunnels: [] }),
    // The Activity badge of the sidebar.
    "GET /api/v1/jobs": () => json({ jobs: [] }),
  });
}

function heading() {
  return screen.getByRole("heading", { level: 1 });
}

function link(name: string) {
  return screen.getByRole("link", { name });
}

function goTo(hash: string) {
  act(() => {
    window.location.hash = hash;
  });
}

describe("App", () => {
  beforeEach(() => {
    devEngine();
  });

  it("opens Instances without a hash", async () => {
    render(<App />);
    expect(heading()).toHaveTextContent("Instances");
    expect(link("Instances")).toHaveAttribute("aria-current", "page");
    expect(await screen.findByText("engine dev")).toBeInTheDocument();
    expect(await screen.findByText("No instances yet")).toBeInTheDocument();
  });

  it("opens the page in the hash", () => {
    window.location.hash = "#settings";
    render(<App />);
    expect(heading()).toHaveTextContent("Settings");
    expect(link("Settings")).toHaveAttribute("aria-current", "page");
  });

  it.each(["#databases", "#databases/dev-sql"])("opens Databases from %s", async (hash) => {
    window.location.hash = hash;
    render(<App />);
    expect(heading()).toHaveTextContent("Databases");
    expect(link("Databases")).toHaveAttribute("aria-current", "page");
    // The fake engine has no targets.
    expect(await screen.findByText("No instances configured")).toBeInTheDocument();
  });

  it("opens About from #about", () => {
    window.location.hash = "#about";
    render(<App />);
    expect(heading()).toHaveTextContent("About");
    expect(link("About")).toHaveAttribute("aria-current", "page");
  });

  it("follows the hash as it changes", async () => {
    render(<App />);
    goTo("#settings");
    expect(await screen.findByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
    expect(link("Instances")).not.toHaveAttribute("aria-current");

    goTo("#instances");
    expect(await screen.findByRole("heading", { level: 1, name: "Instances" })).toBeInTheDocument();

    goTo("#settings");
    await screen.findByRole("heading", { level: 1, name: "Settings" });
    goTo("#databases/prod-sql");
    expect(await screen.findByRole("heading", { level: 1, name: "Databases" })).toBeInTheDocument();
    goTo("#databasesx");
    expect(await screen.findByRole("heading", { level: 1, name: "Instances" })).toBeInTheDocument();

    goTo("#settings");
    await screen.findByRole("heading", { level: 1, name: "Settings" });
    goTo("#unknown");
    expect(await screen.findByRole("heading", { level: 1, name: "Instances" })).toBeInTheDocument();
  });

  it("moves the focus to the title of a new page, not on start", async () => {
    render(<App />);
    const start = heading();
    expect(start).not.toHaveFocus();
    expect(document.title).toBe("Instances · Dialga");

    goTo("#databases");
    const title = await screen.findByRole("heading", { level: 1, name: "Databases" });
    // The hashchange renders outside act: the focus comes with the passive effect.
    await waitFor(() => expect(title).toHaveFocus());
    expect(title).toHaveAttribute("tabindex", "-1");
    expect(document.title).toBe("Databases · Dialga");
  });

  it("skips the navigation to the page", async () => {
    render(<App />);
    await userEvent.click(screen.getByRole("button", { name: "Skip to content" }));
    expect(screen.getByRole("main")).toHaveFocus();
  });

  it("navigates from the sidebar links", async () => {
    render(<App />);
    await userEvent.click(link("Settings"));
    expect(await screen.findByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
    expect(window.location.hash).toBe("#settings");
    await userEvent.click(link("Instances"));
    expect(await screen.findByRole("heading", { level: 1, name: "Instances" })).toBeInTheDocument();
  });

  it("explains how to reach the engine from the browser", async () => {
    vi.stubEnv("VITE_ENGINE_URL", "");
    vi.stubEnv("VITE_ENGINE_TOKEN", "");
    render(<App />);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Set VITE_ENGINE_URL and VITE_ENGINE_TOKEN to use the browser dev server",
    );
    expect(screen.getByText("Engine not available")).toBeInTheDocument();
  });

  it("keeps the language on every page", async () => {
    window.location.hash = "#settings";
    render(<App />);
    await userEvent.click(screen.getByRole("radio", { name: "Italiano" }));
    expect(link("Istanze")).toBeInTheDocument();
    goTo("#instances");
    expect(await screen.findByRole("heading", { level: 1, name: "Istanze" })).toBeInTheDocument();
  });

  describe("theme", () => {
    it("follows the system by default", () => {
      const system = installMatchMedia(true);
      render(<App />);
      expect(document.documentElement.dataset.theme).toBe("dark");
      act(() => system.setDark(false));
      expect(document.documentElement.dataset.theme).toBe("light");
    });

    it("applies and saves the chosen theme", async () => {
      const system = installMatchMedia(false);
      window.location.hash = "#settings";
      render(<App />);
      expect(system.listeners()).toBe(1);

      await userEvent.click(screen.getByRole("radio", { name: "Dark" }));
      expect(document.documentElement.dataset.theme).toBe("dark");
      expect(localStorage.getItem("dialga.theme")).toBe("dark");
      // A fixed theme stops following the system.
      expect(system.listeners()).toBe(0);
      act(() => system.setDark(false));
      expect(document.documentElement.dataset.theme).toBe("dark");

      await userEvent.click(screen.getByRole("radio", { name: "Light" }));
      expect(document.documentElement.dataset.theme).toBe("light");
      expect(localStorage.getItem("dialga.theme")).toBe("light");
    });

    it("starts with the saved theme", () => {
      installMatchMedia(false);
      localStorage.setItem("dialga.theme", "dark");
      window.location.hash = "#settings";
      render(<App />);
      expect(document.documentElement.dataset.theme).toBe("dark");
      expect(screen.getByRole("radio", { name: "Dark" })).toBeChecked();
    });
  });
});
