import { invoke, isTauri } from "@tauri-apps/api/core";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AboutPage } from "./AboutPage";
import { renderI18n } from "./test/fixtures";
import { ENGINE, json, mockFetch } from "./test/http";
import { UpdateNotice } from "./UpdateNotice";
import { UpdateProvider } from "./UpdateProvider";

vi.mock("@tauri-apps/api/core", () => ({ invoke: vi.fn(), isTauri: vi.fn(() => false) }));

afterEach(() => {
  vi.mocked(invoke).mockReset();
  vi.mocked(isTauri).mockReturnValue(false);
});

const URL_120 = "https://github.com/fpellizz/dialga/releases/tag/v1.2.0";
const NEWER = { current: "1.0.0", latest: "1.2.0", newer: true, url: URL_120 };

function renderNotice(collapsed = false, lang: "en" | "it" = "en") {
  mockFetch({ "GET /api/v1/update": () => json(NEWER) });
  return renderI18n(
    <UpdateProvider engine={ENGINE}>
      <UpdateNotice collapsed={collapsed} />
    </UpdateProvider>,
    lang,
  );
}

describe("UpdateNotice", () => {
  it("shows nothing when there is no newer release", async () => {
    mockFetch({ "GET /api/v1/update": () => json({ current: "1.0.0", newer: false }) });
    renderI18n(
      <UpdateProvider engine={ENGINE}>
        <UpdateNotice collapsed={false} />
      </UpdateProvider>,
    );
    await Promise.resolve();
    expect(screen.queryByRole("region", { name: "Update" })).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toBeEmptyDOMElement();
  });

  it("offers the release as a plain link in the browser and announces it once", async () => {
    renderNotice();
    const region = await screen.findByRole("region", { name: "Update" });
    expect(region).toHaveTextContent("Version 1.2.0 available");
    const link = screen.getByRole("link", { name: "View release" });
    expect(link).toHaveAttribute("href", URL_120);
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noreferrer");
    // One status region, with the sentence: the notice itself is not live.
    expect(screen.getAllByRole("status")).toHaveLength(1);
    expect(screen.getByRole("status")).toHaveTextContent("Version 1.2.0 available");
    expect(region).not.toHaveAttribute("aria-live");
  });

  it("opens the release in the system browser from the desktop app", async () => {
    vi.mocked(isTauri).mockReturnValue(true);
    renderNotice();
    await userEvent.click(await screen.findByRole("link", { name: "View release" }));
    expect(invoke).toHaveBeenCalledWith("open_release", { tag: "v1.2.0" });
  });

  it("can be dismissed for that version", async () => {
    renderNotice();
    await userEvent.click(await screen.findByRole("button", { name: "Dismiss" }));
    expect(screen.queryByRole("region", { name: "Update" })).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toBeEmptyDOMElement();
    expect(localStorage.getItem("dialga.updates.dismissed")).toBe("1.2.0");
  });

  it("collapsed: a dot with an accessible name, no dismiss button", async () => {
    renderNotice(true);
    const dot = await screen.findByRole("link", { name: "Version 1.2.0 available. View release" });
    expect(dot).toHaveAttribute("href", URL_120);
    expect(dot).toHaveAttribute("title", "Version 1.2.0 available");
    expect(screen.queryByRole("button", { name: "Dismiss" })).not.toBeInTheDocument();
  });

  it("speaks Italian", async () => {
    renderNotice(false, "it");
    expect(
      await screen.findByText("Versione 1.2.0 disponibile", { selector: "p" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Nascondi" })).toBeInTheDocument();
  });
});

describe("About page", () => {
  it("lists the new version with a link, even after the notice was dismissed", async () => {
    localStorage.setItem("dialga.updates.dismissed", "1.2.0");
    mockFetch({ "GET /api/v1/update": () => json(NEWER) });
    renderI18n(
      <UpdateProvider engine={ENGINE}>
        <AboutPage engine={ENGINE} />
      </UpdateProvider>,
    );
    await waitFor(() =>
      expect(screen.getByText("Update").nextElementSibling).toHaveTextContent(
        "Version 1.2.0 available",
      ),
    );
    expect(screen.getByRole("link", { name: "View release" })).toHaveAttribute("href", URL_120);
  });

  it("has no update row when nothing is new", () => {
    renderI18n(<AboutPage engine={ENGINE} />);
    expect(screen.queryByText("Update")).not.toBeInTheDocument();
  });
});
