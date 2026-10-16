import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Task } from "./api";
import { TasksPanel } from "./TasksPanel";
import { BACKUP_FAILED, RESTORED, RESTORING, renderI18n } from "./test/fixtures";
import { deferred, ENGINE, json, mockFetch, text } from "./test/http";

const TASKS = "GET /api/v1/targets/dev-sql/tasks";
const POLL_MS = 30_000;

/** Answers the task lists in order, then the last one forever. */
function serve(...lists: Task[][]) {
  return mockFetch({
    [TASKS]: (_req, call) => json({ tasks: lists[Math.min(call, lists.length - 1)] }),
  });
}

function panel() {
  return screen.getByRole("region", { name: "Native backup and restore tasks" });
}

const tick = (ms: number) => act(() => vi.advanceTimersByTimeAsync(ms));

describe("TasksPanel", () => {
  it("shows a skeleton until the first answer", async () => {
    const answer = deferred<Response>();
    mockFetch({ [TASKS]: () => answer.promise });
    const { container } = renderI18n(<TasksPanel engine={ENGINE} target="dev-sql" />);

    expect(panel()).toHaveAttribute("aria-busy", "true");
    expect(container.querySelectorAll(".skeleton")).toHaveLength(2);
    expect(screen.getByRole("button", { name: "Refresh" })).toBeDisabled();

    answer.resolve(json({ tasks: [] }));
    expect(await screen.findByText("No native tasks in the last 36 days.")).toBeInTheDocument();
    expect(container.querySelector(".skeleton")).toBeNull();
    expect(panel()).toHaveAttribute("aria-busy", "false");
  });

  it("asks the engine with the session token", async () => {
    const fetch = serve([]);
    renderI18n(<TasksPanel engine={ENGINE} target="dev-sql" />);
    await screen.findByText("No native tasks in the last 36 days.");
    const [url, init] = fetch.mock.calls[0];
    expect(String(url)).toBe(`${ENGINE.url}/api/v1/targets/dev-sql/tasks`);
    expect(init?.headers).toEqual({ Authorization: `Bearer ${ENGINE.token}` });
  });

  it("lists the tasks, with progress only for the running ones", async () => {
    serve([RESTORING, BACKUP_FAILED]);
    renderI18n(<TasksPanel engine={ENGINE} target="dev-sql" />);

    const [running, failed] = await screen.findAllByRole("listitem");
    expect(within(running).getByText("#7")).toBeInTheDocument();
    expect(within(running).getByText("RESTORE_DB")).toBeInTheDocument();
    expect(within(running).getByText("sales")).toBeInTheDocument();
    expect(within(running).getByText("In progress")).toHaveClass("badge-running");
    expect(within(running).getByRole("progressbar", { name: "RESTORE_DB sales" })).toHaveAttribute(
      "aria-valuenow",
      "40",
    );
    expect(within(running).getByText("Updated 2026-10-16 09:41 · 40%")).toBeInTheDocument();
    expect(within(running).getByText("Details")).toBeInTheDocument();
    expect(running.querySelector("pre")?.textContent).toBe(
      "Task execution has started.\nsales.bak: 40% processed.",
    );

    expect(within(failed).getByText("Failed")).toHaveClass("badge-failed");
    expect(within(failed).queryByRole("progressbar")).toBeNull();
    expect(within(failed).getByText("Updated 2026-10-15 18:02")).toBeInTheDocument();
    expect(within(failed).queryByText("Details")).toBeNull();

    expect(
      screen.getByText("Refreshing every 30 seconds while a task is running."),
    ).toBeInTheDocument();
  });

  it("translates the lifecycles", async () => {
    serve([RESTORING, BACKUP_FAILED]);
    renderI18n(<TasksPanel engine={ENGINE} target="dev-sql" />, "it");
    expect(await screen.findByText("In corso")).toBeInTheDocument();
    expect(screen.getByText("Fallito")).toBeInTheDocument();
    expect(screen.getByText("Aggiornato 2026-10-15 18:02")).toBeInTheDocument();
  });

  it.each([
    [
      "an engine error",
      () => text("RDS instance my-sqlserver: not found\n", 502),
      "RDS instance my-sqlserver: not found",
    ],
    ["an empty error", () => text("", 500), "HTTP 500"],
    ["a network error", () => Promise.reject(new TypeError("Failed to fetch")), "Failed to fetch"],
  ])("shows %s without the Error: prefix", async (_name, answer, message) => {
    mockFetch({ [TASKS]: answer });
    const { container } = renderI18n(<TasksPanel engine={ENGINE} target="dev-sql" />);
    expect(await screen.findByRole("alert")).toHaveTextContent(new RegExp(`^${message}$`));
    expect(container.querySelector(".skeleton")).toBeNull();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeEnabled();
  });

  it("refreshes on demand and clears an old error", async () => {
    const fetch = mockFetch({
      [TASKS]: (_req, call) =>
        call === 0 ? text("SQL password: not set\n", 422) : json({ tasks: [] }),
    });
    renderI18n(<TasksPanel engine={ENGINE} target="dev-sql" />);
    expect(await screen.findByRole("alert")).toHaveTextContent("SQL password: not set");

    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByText("No native tasks in the last 36 days.")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it("aborts the request in flight when it goes away", async () => {
    const fetch = mockFetch({ [TASKS]: () => new Promise<Response>(() => {}) });
    const { unmount } = renderI18n(<TasksPanel engine={ENGINE} target="dev-sql" />);
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
    const signal = fetch.mock.calls[0][1]?.signal;
    expect(signal?.aborted).toBe(false);
    unmount();
    expect(signal?.aborted).toBe(true);
  });

  it("drops the answer for the previous target", async () => {
    const fetch = mockFetch({
      [TASKS]: () => new Promise<Response>(() => {}),
      "GET /api/v1/targets/prod-sql/tasks": () => json({ tasks: [BACKUP_FAILED] }),
    });
    const { rerender } = renderI18n(<TasksPanel engine={ENGINE} target="dev-sql" />);
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));

    rerender(<TasksPanel engine={ENGINE} target="prod-sql" />);
    expect(await screen.findByText("hr")).toBeInTheDocument();
    expect(fetch.mock.calls[0][1]?.signal?.aborted).toBe(true);
    // The aborted request must not reset the state of the one that replaced it.
    expect(panel()).toHaveAttribute("aria-busy", "false");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  describe("polling", () => {
    beforeEach(() => {
      // Fake timers that also follow the real clock, so findBy/waitFor still work.
      vi.useFakeTimers({ shouldAdvanceTime: true });
    });
    afterEach(() => {
      vi.useRealTimers();
    });

    it("polls every 30 seconds while a task is active and stops after", async () => {
      const fetch = serve([RESTORING], [RESTORING], [RESTORED]);
      renderI18n(<TasksPanel engine={ENGINE} target="dev-sql" />);
      expect(await screen.findByText("In progress")).toBeInTheDocument();
      expect(fetch).toHaveBeenCalledTimes(1);

      await tick(POLL_MS);
      await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
      expect(screen.getByText("In progress")).toBeInTheDocument();

      await tick(POLL_MS);
      expect(await screen.findByText("Succeeded")).toBeInTheDocument();
      expect(fetch).toHaveBeenCalledTimes(3);
      expect(screen.queryByRole("progressbar")).toBeNull();
      expect(screen.queryByText(/Refreshing every 30 seconds/)).toBeNull();

      await tick(5 * POLL_MS);
      expect(fetch).toHaveBeenCalledTimes(3);
    });

    it("does not poll when every task is finished", async () => {
      const fetch = serve([RESTORED, BACKUP_FAILED]);
      renderI18n(<TasksPanel engine={ENGINE} target="dev-sql" />);
      expect(await screen.findByText("Succeeded")).toBeInTheDocument();
      await tick(5 * POLL_MS);
      expect(fetch).toHaveBeenCalledTimes(1);
    });

    it("sends one request per interval under StrictMode", async () => {
      const fetch = serve([RESTORING]);
      renderI18n(<TasksPanel engine={ENGINE} target="dev-sql" />, "en", true);
      expect(await screen.findByText("In progress")).toBeInTheDocument();

      // StrictMode mounts twice: the first request is aborted, the second one counts.
      expect(fetch).toHaveBeenCalledTimes(2);
      expect(fetch.mock.calls[0][1]?.signal?.aborted).toBe(true);
      expect(fetch.mock.calls[1][1]?.signal?.aborted).toBe(false);

      await tick(POLL_MS);
      await waitFor(() => expect(fetch).toHaveBeenCalledTimes(3));
      await tick(POLL_MS);
      await waitFor(() => expect(fetch).toHaveBeenCalledTimes(4));
      await tick(POLL_MS / 2);
      expect(fetch).toHaveBeenCalledTimes(4);
    });

    it("skips a tick while the previous request is still running", async () => {
      const slow = deferred<Response>();
      const fetch = mockFetch({
        [TASKS]: (_req, call) => (call === 0 ? json({ tasks: [RESTORING] }) : slow.promise),
      });
      renderI18n(<TasksPanel engine={ENGINE} target="dev-sql" />);
      expect(await screen.findByText("In progress")).toBeInTheDocument();

      await tick(POLL_MS);
      await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
      await tick(POLL_MS);
      expect(fetch).toHaveBeenCalledTimes(2);

      slow.resolve(json({ tasks: [RESTORED] }));
      expect(await screen.findByText("Succeeded")).toBeInTheDocument();
    });
  });
});
