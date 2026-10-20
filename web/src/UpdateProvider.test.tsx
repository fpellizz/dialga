import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { UpdateInfo } from "./api";
import { ENGINE, json, mockFetch, text } from "./test/http";
import { UPDATE_INTERVAL_MS, UpdateProvider, useUpdates } from "./UpdateProvider";

const UPDATE = "GET /api/v1/update";

const NEWER: UpdateInfo = {
  current: "1.0.0",
  latest: "1.2.0",
  newer: true,
  url: "https://github.com/fpellizz/dialga/releases/tag/v1.2.0",
  checkedAt: "2026-10-20T08:00:00Z",
};

function Probe() {
  const u = useUpdates();
  return (
    <div>
      <output data-testid="enabled">{String(u.enabled)}</output>
      <output data-testid="notice">{u.notice?.version ?? "-"}</output>
      <output data-testid="available">{u.available?.version ?? "-"}</output>
      <button type="button" onClick={() => u.setEnabled(!u.enabled)}>
        toggle
      </button>
      <button type="button" onClick={u.dismiss}>
        dismiss
      </button>
      <button type="button" onClick={() => void u.checkNow()}>
        now
      </button>
    </div>
  );
}

const renderProbe = (engine = ENGINE) =>
  render(
    <UpdateProvider engine={engine}>
      <Probe />
    </UpdateProvider>,
  );

describe("UpdateProvider", () => {
  it("checks at start with the cache and reports a newer release", async () => {
    const fetchMock = mockFetch({ [UPDATE]: () => json(NEWER) });
    renderProbe();
    await waitFor(() => expect(screen.getByTestId("notice")).toHaveTextContent("1.2.0"));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(`${ENGINE.url}/api/v1/update`);
  });

  it("does not check without an engine", () => {
    const fetchMock = mockFetch({});
    renderProbe(null as never);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("checks again every 12 hours", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = mockFetch({ [UPDATE]: () => json({ current: "1.0.0", newer: false }) });
    renderProbe();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    await act(() => vi.advanceTimersByTimeAsync(UPDATE_INTERVAL_MS - 1000));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await act(() => vi.advanceTimersByTimeAsync(2000));
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("never calls the engine when the setting is off, and remembers it", async () => {
    localStorage.setItem("dialga.updates.check", "0");
    const fetchMock = mockFetch({ [UPDATE]: () => json(NEWER) });
    renderProbe();
    expect(screen.getByTestId("enabled")).toHaveTextContent("false");
    expect(fetchMock).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "toggle" }));
    expect(localStorage.getItem("dialga.updates.check")).toBe("1");
    await waitFor(() => expect(screen.getByTestId("notice")).toHaveTextContent("1.2.0"));

    await userEvent.click(screen.getByRole("button", { name: "toggle" }));
    expect(localStorage.getItem("dialga.updates.check")).toBe("0");
    expect(screen.getByTestId("notice")).toHaveTextContent("-");
  });

  it("is on by default and survives a storage that throws", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    mockFetch({ [UPDATE]: () => json(NEWER) });
    renderProbe();
    expect(screen.getByTestId("enabled")).toHaveTextContent("true");
    await waitFor(() => expect(screen.getByTestId("notice")).toHaveTextContent("1.2.0"));
    await userEvent.click(screen.getByRole("button", { name: "dismiss" }));
    expect(screen.getByTestId("notice")).toHaveTextContent("-");
  });

  it("dismisses one version, remembers it, and shows a newer one", async () => {
    let answer = NEWER;
    mockFetch({ [UPDATE]: () => json(answer) });
    const { unmount } = renderProbe();
    await waitFor(() => expect(screen.getByTestId("notice")).toHaveTextContent("1.2.0"));
    await userEvent.click(screen.getByRole("button", { name: "dismiss" }));
    expect(screen.getByTestId("notice")).toHaveTextContent("-");
    // About still knows about it.
    expect(screen.getByTestId("available")).toHaveTextContent("1.2.0");
    expect(localStorage.getItem("dialga.updates.dismissed")).toBe("1.2.0");
    unmount();

    renderProbe();
    await waitFor(() => expect(screen.getByTestId("available")).toHaveTextContent("1.2.0"));
    expect(screen.getByTestId("notice")).toHaveTextContent("-");

    answer = {
      ...NEWER,
      latest: "1.3.0",
      url: "https://github.com/fpellizz/dialga/releases/tag/v1.3.0",
    };
    await userEvent.click(screen.getByRole("button", { name: "now" }));
    await waitFor(() => expect(screen.getByTestId("notice")).toHaveTextContent("1.3.0"));
  });

  it("stays quiet when the engine fails or sends a strange version", async () => {
    mockFetch({ [UPDATE]: () => text("boom\n", 500) });
    renderProbe();
    await act(() => Promise.resolve());
    expect(screen.getByTestId("notice")).toHaveTextContent("-");
  });

  it("ignores a latest that is not X.Y.Z", async () => {
    mockFetch({ [UPDATE]: () => json({ ...NEWER, latest: "1.2.0/../x" }) });
    renderProbe();
    await act(() => Promise.resolve());
    expect(screen.getByTestId("notice")).toHaveTextContent("-");
  });

  it("asks GitHub again with force=1 on 'Check now'", async () => {
    const fetchMock = mockFetch({ [UPDATE]: () => json({ current: "1.0.0", newer: false }) });
    renderProbe();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    await userEvent.click(screen.getByRole("button", { name: "now" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(String(fetchMock.mock.calls[1]?.[0])).toBe(`${ENGINE.url}/api/v1/update?force=1`);
  });
});
