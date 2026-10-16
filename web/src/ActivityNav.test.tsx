import { act, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { Sidebar } from "./Sidebar";
import { CONFIG_PATH, renderI18n } from "./test/fixtures";
import { ENGINE, json, mockFetch, requestsTo, text } from "./test/http";
import { runningJob, summary } from "./test/jobFixtures";
import { JOBS_POLL_FAST_MS, JOBS_POLL_MS } from "./useJobs";

const JOBS = "GET /api/v1/jobs";
const tick = (ms: number) => act(() => vi.advanceTimersByTimeAsync(ms));

describe("Activity in the sidebar", () => {
  beforeEach(() => {
    mockFetch({ "GET /api/v1/tunnels": () => json({ tunnels: [] }) });
  });

  it("is a link to the Activity page", () => {
    renderI18n(<Sidebar page="activity" engine={ENGINE} engineError="" />);
    const link = screen.getByRole("link", { name: "Activity" });
    expect(link).toHaveAttribute("href", "#activity");
    expect(link).toHaveAttribute("aria-current", "page");
    expect(link.querySelector(".nav-count")).toBeNull();
  });

  it("counts the running jobs", () => {
    renderI18n(<Sidebar page="instances" engine={ENGINE} engineError="" runningJobs={2} />);
    const link = screen.getByRole("link", { name: "Activity, 2 running" });
    expect(link).not.toHaveAttribute("aria-current");
    expect(link.querySelector(".nav-count")).toHaveTextContent("2");
    expect(link.querySelector(".nav-count")).toHaveAttribute("aria-hidden", "true");
  });

  it("speaks Italian", () => {
    renderI18n(<Sidebar page="instances" engine={ENGINE} engineError="" runningJobs={1} />, "it");
    expect(screen.getByRole("link", { name: "Attività, 1 in corso" })).toBeInTheDocument();
  });
});

describe("App and the jobs", () => {
  function devEngine(lists: unknown[][]) {
    vi.stubEnv("VITE_ENGINE_URL", ENGINE.url);
    vi.stubEnv("VITE_ENGINE_TOKEN", ENGINE.token);
    return mockFetch({
      "GET /api/v1/targets": () => json({ configPath: CONFIG_PATH, targets: [] }),
      "GET /api/v1/tunnels": () => json({ tunnels: [] }),
      [JOBS]: (_req, call) => json({ jobs: lists[Math.min(call, lists.length - 1)] }),
    });
  }

  it("polls the jobs for the badge every 15 seconds", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetch = devEngine([[summary(runningJob())], []]);
    render(<App />);
    expect(await screen.findByRole("link", { name: "Activity, 1 running" })).toBeInTheDocument();
    expect(requestsTo(fetch, JOBS)).toHaveLength(1);

    await tick(JOBS_POLL_MS - 100);
    expect(requestsTo(fetch, JOBS)).toHaveLength(1);
    await tick(100);
    await waitFor(() => expect(screen.getByRole("link", { name: "Activity" })).toBeInTheDocument());
    expect(requestsTo(fetch, JOBS)).toHaveLength(2);
  });

  it("opens Activity from the hash and polls faster there while a job runs", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    window.location.hash = "#activity";
    const fetch = devEngine([[summary(runningJob())]]);
    render(<App />);
    expect(await screen.findByRole("heading", { level: 1, name: "Activity" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: /^Import on dev-sql/ })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Activity, 1 running" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    const before = requestsTo(fetch, JOBS).length;
    await tick(JOBS_POLL_FAST_MS);
    await waitFor(() => expect(requestsTo(fetch, JOBS).length).toBe(before + 1));
  });

  it("keeps working when the engine has no job API", async () => {
    vi.stubEnv("VITE_ENGINE_URL", ENGINE.url);
    vi.stubEnv("VITE_ENGINE_TOKEN", ENGINE.token);
    mockFetch({
      "GET /api/v1/targets": () => json({ configPath: CONFIG_PATH, targets: [] }),
      "GET /api/v1/tunnels": () => json({ tunnels: [] }),
      [JOBS]: () => text("not found\n", 404),
    });
    render(<App />);
    expect(await screen.findByText("No instances yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Activity" })).toBeInTheDocument();
  });
});
