import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { SettingsPage } from "./SettingsPage";
import { CONFIG_PATH, renderI18n } from "./test/fixtures";
import { deferred, ENGINE, json, mockFetch, text } from "./test/http";

const TARGETS = "GET /api/v1/targets";

function section(name: string) {
  return screen.getByRole("region", { name });
}

describe("SettingsPage", () => {
  it("switches the language, saves it and updates <html lang>", async () => {
    mockFetch({ [TARGETS]: () => json({ configPath: CONFIG_PATH, targets: [] }) });
    renderI18n(<SettingsPage engine={ENGINE} theme="system" onThemeChange={() => {}} />);
    expect(screen.getByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("en");
    expect(screen.getByRole("radio", { name: "English" })).toBeChecked();

    await userEvent.click(screen.getByRole("radio", { name: "Italiano" }));
    expect(screen.getByRole("heading", { level: 1, name: "Impostazioni" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Italiano" })).toBeChecked();
    expect(document.documentElement.lang).toBe("it");
    expect(localStorage.getItem("dialga.lang")).toBe("it");
    // Language names stay in their own language; the theme names follow the UI.
    expect(screen.getByRole("radio", { name: "English" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Scuro" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: "English" }));
    expect(document.documentElement.lang).toBe("en");
    expect(localStorage.getItem("dialga.lang")).toBe("en");
  });

  it("reports the theme choice and shows the current one", async () => {
    mockFetch({ [TARGETS]: () => json({ configPath: CONFIG_PATH, targets: [] }) });
    const onThemeChange = vi.fn();
    const { rerender } = renderI18n(
      <SettingsPage engine={ENGINE} theme="system" onThemeChange={onThemeChange} />,
    );
    const theme = within(screen.getByRole("group", { name: "Theme" }));
    expect(theme.getAllByRole("radio").map((r) => (r as HTMLInputElement).value)).toEqual([
      "system",
      "light",
      "dark",
    ]);
    expect(theme.getByRole("radio", { name: "System" })).toBeChecked();

    await userEvent.click(theme.getByRole("radio", { name: "Dark" }));
    expect(onThemeChange).toHaveBeenCalledWith("dark");

    rerender(<SettingsPage engine={ENGINE} theme="dark" onThemeChange={onThemeChange} />);
    expect(theme.getByRole("radio", { name: "Dark" })).toBeChecked();
    expect(theme.getByRole("radio", { name: "System" })).not.toBeChecked();
  });

  it("shows a skeleton, then the path of the configuration", async () => {
    // Several cards list the targets: each request gets its own answer.
    const answer = deferred<void>();
    mockFetch({
      [TARGETS]: () => answer.promise.then(() => json({ configPath: CONFIG_PATH, targets: [] })),
    });
    renderI18n(<SettingsPage engine={ENGINE} theme="system" onThemeChange={() => {}} />);
    const config = section("Configuration file");
    expect(config.querySelector(".skeleton")).not.toBeNull();

    answer.resolve();
    expect(await within(config).findByText(CONFIG_PATH)).toHaveClass("path");
    expect(config.querySelector(".skeleton")).toBeNull();
    expect(within(config).queryByRole("alert")).toBeNull();
  });

  it("shows the errors of the configuration next to its path", async () => {
    mockFetch({
      [TARGETS]: () =>
        json({ configPath: CONFIG_PATH, error: "invalid YAML: line 2", targets: [] }),
    });
    renderI18n(<SettingsPage engine={ENGINE} theme="system" onThemeChange={() => {}} />);
    const config = section("Configuration file");
    const banner = await within(config).findByRole("alert");
    expect(banner).toHaveTextContent("The configuration file has errors");
    expect(banner.querySelector("pre")).toHaveTextContent("invalid YAML: line 2");
    expect(within(config).getByText(CONFIG_PATH)).toBeInTheDocument();
  });

  it("shows a failed request without the Error: prefix", async () => {
    mockFetch({ [TARGETS]: () => text("forbidden origin\n", 403) });
    renderI18n(<SettingsPage engine={ENGINE} theme="system" onThemeChange={() => {}} />);
    const banner = await within(section("Configuration file")).findByRole("alert");
    expect(banner.querySelector("pre")).toHaveTextContent(/^forbidden origin$/);
  });

  it("shows the engine version and the local API", async () => {
    mockFetch({ [TARGETS]: () => json({ configPath: CONFIG_PATH, targets: [] }) });
    renderI18n(<SettingsPage engine={ENGINE} theme="system" onThemeChange={() => {}} />);
    const about = section("About");
    expect(within(about).getByText("Engine").nextElementSibling).toHaveTextContent("0.1.0-test");
    expect(within(about).getByText("Local API").nextElementSibling).toHaveTextContent(
      `v1 · ${ENGINE.url}`,
    );
    await within(section("Configuration file")).findByText(CONFIG_PATH);
  });

  it("waits for the engine without asking anything", () => {
    const fetch = mockFetch({});
    renderI18n(<SettingsPage engine={null} theme="system" onThemeChange={() => {}} />);
    const about = section("About");
    expect(within(about).getByText("Engine").nextElementSibling).toHaveTextContent("—");
    expect(within(about).getByText("Local API").nextElementSibling).toHaveTextContent("—");
    expect(fetch).not.toHaveBeenCalled();
  });
});
