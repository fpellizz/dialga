import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { SettingsPage } from "./SettingsPage";
import { CONFIG_PATH, renderI18n } from "./test/fixtures";
import { ENGINE, json, mockFetch, text } from "./test/http";
import { UpdateProvider } from "./UpdateProvider";

const UPDATE = "GET /api/v1/update";
const OTHER = {
  "GET /api/v1/targets": () => json({ configPath: CONFIG_PATH, targets: [] }),
  "GET /api/v1/prerequisites": () => json({ prerequisites: [] }),
};

function renderSettings(update: Parameters<typeof mockFetch>[0][string], lang: "en" | "it" = "en") {
  const fetchMock = mockFetch({ ...OTHER, [UPDATE]: update });
  renderI18n(
    <UpdateProvider engine={ENGINE}>
      <SettingsPage engine={ENGINE} theme="system" onThemeChange={() => {}} />
    </UpdateProvider>,
    lang,
  );
  return fetchMock;
}

/** The status line of the Updates card (the other cards have their own). */
const statusLine = () =>
  within(screen.getByRole("region", { name: "Updates" })).getByRole("status");

const calls = (fetchMock: ReturnType<typeof mockFetch>) =>
  fetchMock.mock.calls.map((c) => String(c[0])).filter((u) => u.includes("/update"));

describe("Settings: Check for updates", () => {
  it("is on by default, shows when it last checked and says nothing yet", async () => {
    renderSettings(() =>
      json({ current: "1.0.0", latest: "1.0.0", newer: false, checkedAt: "2026-10-20T08:00:00Z" }),
    );
    const box = screen.getByRole("checkbox", { name: "Check for updates" });
    expect(box).toBeChecked();
    expect(await screen.findByText(/^Last checked /)).toBeInTheDocument();
    expect(statusLine()).toBeEmptyDOMElement();
  });

  it("turns off: no more calls, the choice is stored, Check now is disabled", async () => {
    const fetchMock = renderSettings(() => json({ current: "1.0.0", newer: false }));
    await waitFor(() => expect(calls(fetchMock)).toHaveLength(1));
    await userEvent.click(screen.getByRole("checkbox", { name: "Check for updates" }));
    expect(screen.getByRole("checkbox", { name: "Check for updates" })).not.toBeChecked();
    expect(localStorage.getItem("dialga.updates.check")).toBe("0");
    expect(screen.getByRole("button", { name: "Check now" })).toBeDisabled();
    expect(calls(fetchMock)).toHaveLength(1);
  });

  it("starts off when the stored choice says so", () => {
    localStorage.setItem("dialga.updates.check", "0");
    const fetchMock = renderSettings(() => json({ current: "1.0.0", newer: false }));
    expect(screen.getByRole("checkbox", { name: "Check for updates" })).not.toBeChecked();
    expect(calls(fetchMock)).toHaveLength(0);
  });

  it("Check now forces the check and reports the result once", async () => {
    let n = 0;
    const fetchMock = renderSettings(() =>
      json(
        ++n === 1
          ? { current: "1.0.0", newer: false }
          : {
              current: "1.0.0",
              latest: "1.2.0",
              newer: true,
              url: "https://github.com/fpellizz/dialga/releases/tag/v1.2.0",
              checkedAt: "2026-10-20T09:00:00Z",
            },
      ),
    );
    await waitFor(() => expect(calls(fetchMock)).toHaveLength(1));
    expect(screen.getByText("Not checked yet")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Check now" }));
    await waitFor(() => expect(statusLine()).toHaveTextContent("Version 1.2.0 available"));
    expect(calls(fetchMock)[1]).toMatch(/\?force=1$/);
    expect(screen.getByText(/^Last checked /)).toBeInTheDocument();
  });

  it("reports an up-to-date app, no release, and a failure", async () => {
    let answer: () => Response = () => json({ current: "1.0.0", latest: "1.0.0", newer: false });
    renderSettings(() => answer());
    const status = statusLine();
    const click = () => userEvent.click(screen.getByRole("button", { name: "Check now" }));
    await click();
    await waitFor(() => expect(status).toHaveTextContent("Dialga is up to date."));
    answer = () => json({ current: "1.0.0", newer: false });
    await click();
    await waitFor(() => expect(status).toHaveTextContent("No release is published yet."));
    answer = () =>
      json({
        current: "1.0.0",
        newer: false,
        error: "update check: GitHub rate limit reached, try again later",
      });
    await click();
    await waitFor(() =>
      expect(status).toHaveTextContent(
        "Could not check for updates: update check: GitHub rate limit",
      ),
    );
    answer = () => text("boom\n", 500);
    await click();
    await waitFor(() => expect(status).toHaveTextContent("Could not check for updates."));
  });

  it("speaks Italian", async () => {
    renderSettings(() => json({ current: "1.0.0", newer: false }), "it");
    expect(screen.getByRole("checkbox", { name: "Controlla gli aggiornamenti" })).toBeChecked();
    expect(screen.getByRole("button", { name: "Controlla ora" })).toBeInTheDocument();
    expect(await screen.findByText("Non ancora controllato")).toBeInTheDocument();
  });
});
