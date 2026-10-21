import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ActivityPage, RECONNECT_MS } from "./ActivityPage";
import { type Job, type JobSummary, logText } from "./jobs";
import { CONFIG_PATH, DEV, renderI18n } from "./test/fixtures";
import { ENGINE, json, mockFetch, requestsTo, sseStream, text } from "./test/http";
import { at, event, interruptedJob, JOB_ID, runningJob, summary } from "./test/jobFixtures";

const JOB = `GET /api/v1/jobs/${JOB_ID}`;
const EVENTS = `GET /api/v1/jobs/${JOB_ID}/events`;
const CANCEL = `POST /api/v1/jobs/${JOB_ID}/cancel`;
const MIB = 1024 * 1024;

function renderList(jobs: JobSummary[] | null, error = "", lang: "en" | "it" = "en") {
  const refresh = vi.fn();
  const view = renderI18n(
    <ActivityPage engine={ENGINE} engineError="" jobs={{ jobs, error, refresh }} />,
    lang,
  );
  const rerender = (next: JobSummary[]) =>
    view.rerender(
      <ActivityPage engine={ENGINE} engineError="" jobs={{ jobs: next, error, refresh }} />,
    );
  return { ...view, refresh, rerender };
}

/** The detail asks for the targets (the protected ones need a confirmation to resume). */
const NO_TARGETS = () => json({ configPath: CONFIG_PATH, targets: [] });

function renderDetail(lang: "en" | "it" = "en") {
  window.location.hash = `#activity/${JOB_ID}`;
  const refresh = vi.fn();
  renderI18n(
    <ActivityPage engine={ENGINE} engineError="" jobs={{ jobs: [], error: "", refresh }} />,
    lang,
  );
  return refresh;
}

function announced(message: string) {
  return screen.getAllByRole("status").some((el) => el.textContent === message);
}

function finished(job: Job, state: Job["state"]): Job {
  return {
    ...job,
    state,
    endedAt: at(11, 30),
    items: job.items.map((i) => ({ ...i, state: "succeeded", percent: 100 })),
  };
}

const tick = (ms: number) => act(() => vi.advanceTimersByTimeAsync(ms));

describe("ActivityPage list", () => {
  it("explains that imports and exports appear here", () => {
    renderList([]);
    expect(screen.getByRole("heading", { level: 1, name: "Activity" })).toBeInTheDocument();
    const empty = screen.getAllByRole("status").find((el) => el.classList.contains("empty"));
    expect(empty).toHaveTextContent("No jobs yet");
    expect(screen.getByText("Previews and dry runs are not recorded here.")).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Show jobs" })).toBeNull();
    expect(
      screen.getByText(
        "Imports, exports, single operations and refused attempts appear here while they run, with every step, and stay here as history.",
      ),
    ).toBeInTheDocument();
  });

  it("shows a skeleton until the first list, then the error if there is one", () => {
    const { container, unmount } = renderList(null);
    expect(container.querySelectorAll(".skeleton")).toHaveLength(2);
    expect(screen.queryByText("No jobs yet")).toBeNull();
    unmount();
    renderList(null, "connection refused");
    expect(screen.getByRole("alert")).toHaveTextContent("connection refused");
  });

  it("lists the running jobs first, then the history", async () => {
    const running = summary(runningJob());
    const failed = summary(runningJob(), {
      id: "20261016T080000Z-00000002",
      kind: "export",
      state: "failed",
      createdAt: at(8, 0),
      endedAt: at(8, 20),
      succeeded: 1,
      failed: 1,
    });
    const done = summary(runningJob(), {
      id: "20261015T080000Z-00000001",
      state: "succeeded",
      createdAt: at(7, 0),
      databases: ["sales"],
      succeeded: 1,
    });
    const { refresh } = renderList([done, failed, running]);

    const now = screen.getByRole("region", { name: "Running" });
    const link = within(now).getByRole("link", { name: /^Import on dev-sql/ });
    expect(link).toHaveAttribute("href", `#activity/${JOB_ID}`);
    expect(within(link).getByText("Running")).toHaveClass("badge-running");
    expect(within(link).getByText("2 databases")).toBeInTheDocument();
    expect(within(link).getByText("Created 2026-10-16 11:00")).toBeInTheDocument();
    expect(
      within(link).getByRole("progressbar", { name: "0 of 2 databases done" }),
    ).toHaveAttribute("aria-valuemax", "2");

    const history = within(screen.getByRole("region", { name: "History" })).getAllByRole("link");
    expect(history.map((l) => l.getAttribute("href"))).toEqual([
      `#activity/${failed.id}`,
      `#activity/${done.id}`,
    ]);
    expect(within(history[0]).getByText("Export on dev-sql")).toBeInTheDocument();
    expect(within(history[0]).getByText("Failed")).toHaveClass("badge-failed");
    expect(within(history[0]).getByText("1 failed")).toBeInTheDocument();
    expect(within(history[0]).getByText("Ended 2026-10-16 08:20")).toBeInTheDocument();
    expect(within(history[0]).queryByRole("progressbar")).toBeNull();
    expect(within(history[1]).getByText("1 database")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("announces the end of a job, not its progress", () => {
    const running = summary(runningJob());
    const { rerender } = renderList([running]);
    rerender([{ ...running, succeeded: 1 }]);
    expect(announced("Import on dev-sql: Succeeded.")).toBe(false);
    rerender([{ ...running, state: "succeeded", succeeded: 2, endedAt: at(11, 30) }]);
    expect(announced("Import on dev-sql: Succeeded.")).toBe(true);
  });

  it("speaks Italian", () => {
    renderList([summary(runningJob()), summary(interruptedJob(), { id: "x" })], "", "it");
    expect(screen.getByRole("heading", { level: 1, name: "Attività" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "In corso" })).toBeInTheDocument();
    const history = screen.getByRole("region", { name: "Storico" });
    expect(within(history).getByText("Import su dev-sql")).toBeInTheDocument();
    expect(within(history).getByText("Interrotto")).toBeInTheDocument();
    expect(
      screen.getByRole("progressbar", { name: "0 di 2 database completati" }),
    ).toBeInTheDocument();
  });

  it("opens a job when the hash names it", async () => {
    mockFetch({ [JOB]: () => json(interruptedJob()), "GET /api/v1/targets": NO_TARGETS });
    renderList([summary(interruptedJob())]);
    act(() => {
      window.location.hash = `#activity/${JOB_ID}`;
    });
    expect(
      await screen.findByRole("heading", { level: 1, name: "Import on dev-sql" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "All jobs" })).toHaveAttribute("href", "#activity");
  });
});

describe("ActivityPage detail", () => {
  afterEach(() => {
    Reflect.deleteProperty(window.navigator, "clipboard");
  });

  it("shows the steps of a swap in Italian and a plain restore as the engine titles them", async () => {
    const swap = runningJob();
    swap.items[0] = {
      name: "sales",
      state: "running",
      step: "rename-old",
      percent: 40,
      task: { id: 7, type: "RESTORE_DB", lifecycle: "IN_PROGRESS", percent: 40 },
      steps: [
        {
          id: "restore",
          title: "Restore sales_tmp from s3://b/in/sales.bak (temporary name)",
          status: "succeeded",
        },
        {
          id: "rename-old",
          title: "Rename sales to sales_old (the current database is kept)",
          status: "running",
        },
        {
          id: "rename-new",
          title: "Rename sales_tmp to sales (the restored database takes its place)",
          status: "pending",
        },
      ],
    };
    mockFetch({ [JOB]: () => json(swap), [EVENTS]: () => sseStream().response });
    renderDetail("it");
    const steps = await screen.findByRole("list", { name: "Passaggi di sales" });
    expect(
      within(steps)
        .getAllByRole("listitem")
        .map((li) => li.querySelector("span:nth-child(2)")?.textContent),
    ).toEqual([
      "Ripristina sales accanto a quello attuale (nome temporaneo)",
      "Rinomina sales col nome vecchio (il database attuale resta)",
      "Rinomina il database ripristinato in sales",
    ]);
    expect(
      screen.getAllByText("Rinomina sales col nome vecchio (il database attuale resta)"),
    ).toHaveLength(2);
    expect(
      screen.getByRole("progressbar", {
        name: "sales: Rinomina sales col nome vecchio (il database attuale resta)",
      }),
    ).toBeInTheDocument();
  });

  it("keeps the engine titles of a plain restore", async () => {
    mockFetch({ [JOB]: () => json(runningJob()), [EVENTS]: () => sseStream().response });
    renderDetail("it");
    const steps = await screen.findByRole("list", { name: "Passaggi di sales" });
    expect(within(steps).getByText("Restore sales")).toBeInTheDocument();
  });

  it("follows a running job live, then reads it once more at the end", async () => {
    const stream = sseStream();
    const job = runningJob();
    const fetch = mockFetch({
      [JOB]: (_req, call) => json(call === 0 ? job : finished(job, "succeeded")),
      [EVENTS]: () => stream.response,
    });
    const refresh = renderDetail();

    expect(
      await screen.findByRole("heading", { level: 1, name: "Import on dev-sql" }),
    ).toBeInTheDocument();
    expect(screen.getAllByText("Running")[0]).toHaveClass("badge-running");
    await waitFor(() => expect(requestsTo(fetch, EVENTS)).toHaveLength(1));
    expect(requestsTo(fetch, EVENTS)[0].headers).toMatchObject({ "Last-Event-ID": "3" });
    expect(screen.getByText("Live")).toBeInTheDocument();

    const steps = screen.getByRole("list", { name: "Steps of sales" });
    expect(
      within(steps)
        .getAllByRole("listitem")
        .map((li) => li.textContent),
    ).toEqual(["RunningUpload sales.bak", "PendingRestore sales"]);

    act(() =>
      stream.send(
        "upload.progress",
        event(4, "upload.progress", {
          item: "sales",
          step: "upload",
          transfer: {
            direction: "upload",
            bytes: 40 * MIB,
            total: 100 * MIB,
            bytesPerSecond: 10 * MIB,
            etaSeconds: 6,
          },
        }),
      ),
    );
    const bar = await screen.findByRole("progressbar", { name: "sales: Upload sales.bak" });
    expect(bar).toHaveAttribute("aria-valuenow", "40");
    expect(screen.getByText("40 MiB of 100 MiB · 10 MiB/s · 6 s left")).toBeInTheDocument();
    // Progress is not logged, and it is not announced.
    expect(
      screen.getAllByRole("listitem").filter((li) => li.classList.contains("job-log-line")),
    ).toHaveLength(3);
    expect(announced("Import on dev-sql: Succeeded.")).toBe(false);

    act(() => {
      stream.send(
        "step.succeeded",
        event(5, "step.succeeded", { item: "sales", step: "upload", message: "100 MiB uploaded" }),
      );
      stream.send(
        "step.started",
        event(6, "step.started", { item: "sales", step: "restore", message: "Restore sales" }),
      );
      stream.send(
        "task.status",
        event(7, "task.status", {
          item: "sales",
          step: "restore",
          task: { id: 7, type: "RESTORE_DB", lifecycle: "IN_PROGRESS", percent: 25 },
        }),
      );
    });
    expect(await screen.findByText("Native task #7 · In progress · 25%")).toBeInTheDocument();
    expect(screen.getByRole("progressbar", { name: "sales: Restore sales" })).toHaveAttribute(
      "aria-valuenow",
      "25",
    );
    expect(within(steps).getByText("100 MiB uploaded")).toBeInTheDocument();

    act(() => {
      stream.send("job.succeeded", event(8, "job.succeeded", { message: "2 database(s) done." }));
      stream.close();
    });
    await waitFor(() => expect(announced("Import on dev-sql: Succeeded.")).toBe(true));
    await waitFor(() => expect(requestsTo(fetch, JOB)).toHaveLength(2));
    expect(screen.getAllByText("Succeeded")[0]).toHaveClass("badge-ok");
    expect(screen.queryByRole("button", { name: "Cancel job" })).toBeNull();
    expect(screen.queryByText("Live")).toBeNull();
    expect(refresh).toHaveBeenCalled();
    expect(requestsTo(fetch, EVENTS)).toHaveLength(1);
  });

  it("reconnects after the last event it saw", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const first = sseStream();
    const second = sseStream();
    const fetch = mockFetch({
      [JOB]: (_req, call) => json(call === 0 ? runningJob() : finished(runningJob(), "succeeded")),
      [EVENTS]: (_req, call) => (call === 0 ? first.response : second.response),
    });
    renderDetail();
    await screen.findByRole("heading", { level: 1, name: "Import on dev-sql" });
    await waitFor(() => expect(requestsTo(fetch, EVENTS)).toHaveLength(1));

    act(() => {
      first.send("log", event(4, "log", { message: "part 1 done" }));
      first.close(); // e.g. the client fell behind
    });
    expect(await screen.findByText("Connection lost, reconnecting…")).toBeInTheDocument();
    await tick(RECONNECT_MS);
    await waitFor(() => expect(requestsTo(fetch, EVENTS)).toHaveLength(2));
    expect(requestsTo(fetch, EVENTS)[1].headers).toMatchObject({ "Last-Event-ID": "4" });

    act(() => {
      second.send("log", event(4, "log", { message: "part 1 done" })); // replayed: shown once
      second.send("log", event(5, "log", { message: "part 2 done" }));
    });
    expect(await screen.findByText("part 2 done")).toBeInTheDocument();
    expect(screen.getAllByText("part 1 done")).toHaveLength(1);
    expect(screen.getByText("Live")).toBeInTheDocument();
    act(() => {
      second.send("job.succeeded", event(6, "job.succeeded"));
      second.close();
    });
    await waitFor(() => expect(requestsTo(fetch, JOB)).toHaveLength(2));
  });

  it("cancels after the confirmation, which says that native tasks keep running", async () => {
    const user = userEvent.setup();
    const stream = sseStream();
    const fetch = mockFetch({
      [JOB]: () => json(runningJob()),
      [EVENTS]: () => stream.response,
      [CANCEL]: () => json(summary(runningJob(), { cancelRequested: true }), 202),
    });
    const refresh = renderDetail();
    await user.click(await screen.findByRole("button", { name: "Cancel job" }));

    const box = screen.getByRole("region", { name: "Cancel this job?" });
    expect(box).toHaveAccessibleDescription(
      "Databases that are queued or waiting do not start, and running uploads stop. A native RDS task already running on the instance is not stopped: it ends on its own. With a swap or a drop, the database may be left in the intermediate state shown in the steps.",
    );
    expect(within(box).getByRole("button", { name: "Keep running" })).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("region", { name: "Cancel this job?" })).toBeNull();
    expect(requestsTo(fetch, CANCEL)).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Cancel job" }));
    await user.click(
      within(screen.getByRole("region", { name: "Cancel this job?" })).getByRole("button", {
        name: "Cancel job",
      }),
    );
    const banner = await screen.findByText(/^Cancelling: the running steps are stopping/);
    expect(banner.closest(".banner")).toHaveFocus();
    expect(requestsTo(fetch, CANCEL)).toHaveLength(1);
    expect(screen.queryByRole("button", { name: "Cancel job" })).toBeNull();
    expect(refresh).toHaveBeenCalled();
  });

  it("shows why the cancel failed", async () => {
    const user = userEvent.setup();
    mockFetch({
      [JOB]: () => json(runningJob()),
      [EVENTS]: () => sseStream().response,
      [CANCEL]: () => text("job already ended (succeeded)\n", 409),
    });
    renderDetail();
    await user.click(await screen.findByRole("button", { name: "Cancel job" }));
    await user.click(
      within(screen.getByRole("region", { name: "Cancel this job?" })).getByRole("button", {
        name: "Cancel job",
      }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent("job already ended (succeeded)");
    expect(screen.getByRole("region", { name: "Cancel this job?" })).toBeInTheDocument();
  });

  it("shows what a resume needs for an interrupted job, without following it", async () => {
    const fetch = mockFetch({
      [JOB]: () => json(interruptedJob()),
      "GET /api/v1/targets": NO_TARGETS,
    });
    renderDetail();
    const panel = await screen.findByRole("region", {
      name: "Interrupted: the engine stopped while this job was running.",
    });
    expect(
      within(panel).getByText("Resume continues it from what the journal kept:"),
    ).toBeInTheDocument();
    expect([...panel.querySelectorAll("dl")].map((dl) => dl.getAttribute("aria-label"))).toEqual([
      "sales",
      "hr",
    ]);
    expect(
      within(panel).getByText(
        "s3://my-sqlserver-backups/import/sales.bak · upload id up-9 · 4 of 10 parts",
      ),
    ).toBeInTheDocument();
    expect(within(panel).getByText("Upload sales.bak, Restore sales")).toBeInTheDocument();
    expect(within(panel).getByText("#12 (still running on the instance)")).toBeInTheDocument();
    expect(
      screen.getByText(
        "Native task #12 was still running on the instance when the job stopped: Dialga does not stop it.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Native task 12 (IN_PROGRESS) is still running on the instance"),
    ).toHaveClass("tone-warning");
    expect(screen.queryByRole("button", { name: "Cancel job" })).toBeNull();
    expect(screen.queryByRole("progressbar")).toBeNull();
    expect(requestsTo(fetch, EVENTS)).toHaveLength(0);
  });

  it("says when the job is not there", async () => {
    mockFetch({ [JOB]: () => text("job not found\n", 404) });
    renderDetail();
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "No job with this id: the history keeps the last 200 jobs, for 30 days at most.",
    );
    expect(screen.getByRole("link", { name: "All jobs" })).toHaveAttribute("href", "#activity");
  });

  it("copies the log", async () => {
    const user = userEvent.setup();
    const writeText = vi
      .fn()
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error("denied"));
    Object.defineProperty(window.navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    mockFetch({ [JOB]: () => json(interruptedJob()), "GET /api/v1/targets": NO_TARGETS });
    renderDetail();
    await user.click(await screen.findByRole("button", { name: "Copy the log" }));
    expect(writeText).toHaveBeenCalledWith(logText(interruptedJob()));
    expect(await screen.findByText("Log copied.")).toHaveAttribute("role", "status");

    await user.click(screen.getByRole("button", { name: "Copy the log" }));
    expect(
      await screen.findByText("Copy failed: select the log and copy it by hand."),
    ).toBeInTheDocument();
  });

  it("speaks Italian", async () => {
    mockFetch({ [JOB]: () => json(interruptedJob()), "GET /api/v1/targets": NO_TARGETS });
    window.location.hash = `#activity/${JOB_ID}`;
    renderI18n(
      <ActivityPage
        engine={ENGINE}
        engineError=""
        jobs={{ jobs: [], error: "", refresh: vi.fn() }}
      />,
      "it",
    );
    expect(
      await screen.findByRole("heading", { level: 1, name: "Import su dev-sql" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("region", {
        name: "Interrotto: l'engine si è fermato mentre il job era in corso.",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "s3://my-sqlserver-backups/import/sales.bak · upload id up-9 · 4 di 10 parti",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copia il registro" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Tutti i job" })).toBeInTheDocument();
  });
});

describe("ActivityPage resume", () => {
  const TARGETS = "GET /api/v1/targets";
  const RESUME = `POST /api/v1/jobs/${JOB_ID}/resume`;

  /** The interrupted job queued again by the engine, with its job.resumed event. */
  function resumed(): Job {
    const job = interruptedJob();
    return {
      ...job,
      state: "queued",
      endedAt: undefined,
      items: job.items.map((i) => ({ ...i, state: "queued", taskLeftRunning: false })),
      events: [
        ...job.events,
        event(6, "job.resumed", {
          message: "Resumed: 2 database(s) to finish, 0 kept as they were.",
        }),
      ],
    };
  }

  function targets(isProtected: boolean) {
    return () => json({ configPath: CONFIG_PATH, targets: [{ ...DEV, protected: isProtected }] });
  }

  it("resumes an interrupted job and follows it", async () => {
    const user = userEvent.setup();
    const stream = sseStream();
    const fetch = mockFetch({
      [JOB]: (_req, call) => json(call === 0 ? interruptedJob() : finished(resumed(), "succeeded")),
      [TARGETS]: targets(false),
      [RESUME]: () => json(resumed(), 202),
      [EVENTS]: () => stream.response,
    });
    const refresh = renderDetail();
    await user.click(await screen.findByRole("button", { name: "Resume" }));

    const box = screen.getByRole("form", { name: "Resume this job?" });
    expect(box).toHaveAccessibleDescription(
      "Databases that ended stay as they are. Uploads continue from the parts already in S3, and a native task already queued on the instance is followed, not queued again.",
    );
    expect(within(box).getByRole("button", { name: "Not now" })).toHaveFocus();
    expect(within(box).queryByRole("textbox")).toBeNull();
    await user.click(within(box).getByRole("button", { name: "Resume" }));

    expect(await screen.findByRole("button", { name: "Cancel job" })).toBeInTheDocument();
    expect(requestsTo(fetch, RESUME)[0].body).toBe("{}");
    expect(screen.getByRole("heading", { level: 1, name: "Import on dev-sql" })).toHaveFocus();
    expect(screen.queryByRole("form", { name: "Resume this job?" })).toBeNull();
    expect(
      screen.getByText("Resumed: 2 database(s) to finish, 0 kept as they were."),
    ).toBeInTheDocument();
    expect(refresh).toHaveBeenCalled();
    await waitFor(() => expect(requestsTo(fetch, EVENTS)).toHaveLength(1));
    expect(requestsTo(fetch, EVENTS)[0].headers).toMatchObject({ "Last-Event-ID": "6" });

    act(() => {
      stream.send("job.succeeded", event(7, "job.succeeded"));
      stream.close();
    });
    await waitFor(() => expect(announced("Import on dev-sql: Succeeded.")).toBe(true));
  });

  it("asks to retype the name of a protected target", async () => {
    const user = userEvent.setup();
    const fetch = mockFetch({
      [JOB]: () => json(interruptedJob()),
      [TARGETS]: targets(true),
      [RESUME]: () => json(resumed(), 202),
      [EVENTS]: () => sseStream().response,
    });
    renderDetail();
    await user.click(await screen.findByRole("button", { name: "Resume" }));
    const box = screen.getByRole("form", { name: "Resume this job?" });
    const field = within(box).getByLabelText("Retype dev-sql to confirm: the target is protected");
    expect(field).toHaveFocus();
    const submit = within(box).getByRole("button", { name: "Resume" });
    expect(submit).toBeDisabled();
    await user.type(field, "dev-sq");
    expect(submit).toBeDisabled();
    await user.type(field, "l{Enter}");
    await screen.findByRole("button", { name: "Cancel job" });
    expect(requestsTo(fetch, RESUME)[0].body).toBe('{"confirm":"dev-sql"}');
  });

  it("shows why the resume failed and lets the user leave", async () => {
    const user = userEvent.setup();
    mockFetch({
      [JOB]: () => json(interruptedJob()),
      [TARGETS]: targets(false),
      [RESUME]: () =>
        text("resuming needs the import/export pipeline, which this engine does not have\n", 501),
    });
    renderDetail();
    await user.click(await screen.findByRole("button", { name: "Resume" }));
    await user.click(
      within(screen.getByRole("form", { name: "Resume this job?" })).getByRole("button", {
        name: "Resume",
      }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "resuming needs the import/export pipeline, which this engine does not have",
    );
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("form", { name: "Resume this job?" })).toBeNull();
    expect(screen.getByRole("button", { name: "Resume" })).toBeInTheDocument();
  });

  it("asks for the name when the engine says the target is protected", async () => {
    const user = userEvent.setup();
    mockFetch({
      [JOB]: () => json(interruptedJob()),
      [TARGETS]: () => text("configuration unreadable\n", 500),
      [RESUME]: () => text('not confirmed: this import asks to retype "dev-sql" in confirm\n', 422),
    });
    renderDetail();
    await user.click(await screen.findByRole("button", { name: "Resume" }));
    const box = screen.getByRole("form", { name: "Resume this job?" });
    await user.click(within(box).getByRole("button", { name: "Resume" }));
    expect(await within(box).findByRole("alert")).toHaveTextContent("not confirmed");
    expect(
      within(box).getByLabelText("Retype dev-sql to confirm: the target is protected"),
    ).toBeInTheDocument();
  });

  it("speaks Italian", async () => {
    const user = userEvent.setup();
    mockFetch({ [JOB]: () => json(interruptedJob()), [TARGETS]: targets(true) });
    window.location.hash = `#activity/${JOB_ID}`;
    renderI18n(
      <ActivityPage
        engine={ENGINE}
        engineError=""
        jobs={{ jobs: [], error: "", refresh: vi.fn() }}
      />,
      "it",
    );
    await user.click(await screen.findByRole("button", { name: "Riprendi" }));
    const box = screen.getByRole("form", { name: "Riprendere questo job?" });
    expect(
      within(box).getByLabelText("Riscrivi dev-sql per confermare: il target è protetto"),
    ).toBeInTheDocument();
    expect(within(box).getByRole("button", { name: "Non ora" })).toBeInTheDocument();
  });
});

const REFUSED_ID = "20261016T100000Z-00000003";
const CLI_ID = "20261016T110000Z-00000004";

/** An operation of the Databases page refused at Run, one started by the CLI, a failed import. */
function mixedList(): JobSummary[] {
  const base = runningJob();
  return [
    summary(base),
    summary(base, {
      id: REFUSED_ID,
      kind: "operation",
      operation: "drop",
      state: "failed",
      createdAt: at(10, 0),
      endedAt: at(10, 0),
      databases: ["sales"],
      succeeded: 0,
      failed: 0,
      refusal: { status: 422, reason: "SQL password: not set" },
    }),
    summary(base, {
      id: CLI_ID,
      kind: "operation",
      operation: "finish",
      origin: "cli",
      state: "succeeded",
      createdAt: at(9, 0),
      endedAt: at(9, 5),
      databases: ["hr"],
      succeeded: 1,
      failed: 0,
    }),
    summary(base, {
      id: "20261016T080000Z-00000005",
      kind: "export",
      state: "failed",
      createdAt: at(8, 0),
      endedAt: at(8, 20),
      databases: ["sales"],
      succeeded: 0,
      failed: 1,
    }),
  ];
}

describe("ActivityPage operations, origin and refusals", () => {
  afterEach(() => {
    sessionStorage.clear();
  });

  it("labels every kind of operation with its target and databases", () => {
    const ops = [
      ["restore", "Restore"],
      ["export", "Export database"],
      ["drop", "Drop"],
      ["finish", "Finish restore"],
      ["cancel", "Cancel task"],
      ["post", "Post-restore report"],
    ] as const;
    renderList(
      ops.map(([operation], i) =>
        summary(runningJob(), {
          id: `op-${i}`,
          kind: "operation",
          operation,
          state: "succeeded",
          databases: ["sales", "hr", "ops", "dw"],
        }),
      ),
    );
    for (const [, label] of ops) {
      expect(screen.getByRole("link", { name: new RegExp(`^${label} on dev-sql`) })).toBeVisible();
    }
    expect(screen.getAllByText("sales, hr, ops, +1")).toHaveLength(6);
  });

  it("marks the jobs of the CLI, not those of the app", () => {
    renderList(mixedList());
    const cli = screen.getByRole("link", { name: /^Finish restore on dev-sql/ });
    expect(within(cli).getByText("CLI")).toHaveClass("badge");
    expect(screen.getAllByText("CLI")).toHaveLength(1);
  });

  it("shows a refused job as Refused with its reason and HTTP status", () => {
    renderList(mixedList());
    const row = screen.getByRole("link", { name: /^Drop on dev-sql/ });
    expect(row).toHaveAttribute("href", `#activity/${REFUSED_ID}`);
    expect(within(row).getByText("Refused")).toHaveClass("badge-warning");
    expect(within(row).queryByText("Failed")).toBeNull();
    expect(within(row).getByText("SQL password: not set")).toBeInTheDocument();
    expect(within(row).getByText("HTTP 422 · Not confirmed")).toBeInTheDocument();
    // A failure while running stays Failed.
    const failed = screen.getByRole("link", { name: /^Export on dev-sql/ });
    expect(within(failed).getByText("Failed")).toHaveClass("badge-failed");
  });

  it("filters with chips that carry the counts, by mouse and keyboard", async () => {
    renderList(mixedList());
    const group = screen.getByRole("group", { name: "Show jobs" });
    const chips = within(group).getAllByRole("radio");
    expect(chips.map((c) => c.closest("label")?.textContent)).toEqual([
      "All (4)",
      "Running (1)",
      "Failed (1)",
      "Refused (1)",
    ]);
    expect(within(group).getByRole("radio", { name: "All (4)" })).toBeChecked();

    await userEvent.click(within(group).getByRole("radio", { name: "Refused (1)" }));
    expect(screen.getAllByRole("link")).toHaveLength(1);
    expect(screen.getByRole("link", { name: /^Drop on/ })).toBeVisible();

    // Arrow keys move through the chips and apply them.
    await userEvent.keyboard("{ArrowLeft}");
    expect(within(group).getByRole("radio", { name: "Failed (1)" })).toBeChecked();
    expect(screen.getAllByRole("link")).toHaveLength(1);
    expect(screen.getByRole("link", { name: /^Export on/ })).toBeVisible();
    await userEvent.keyboard("{ArrowLeft}{ArrowLeft}");
    expect(within(group).getByRole("radio", { name: "All (4)" })).toBeChecked();
    expect(screen.getAllByRole("link")).toHaveLength(4);
  });

  it("remembers the chip for the session and says when nothing matches", async () => {
    const first = renderList(mixedList());
    await userEvent.click(screen.getByRole("radio", { name: "Refused (1)" }));
    first.unmount();
    const running = mixedList().filter((j) => j.state !== "failed");
    renderList(running);
    expect(screen.getByRole("radio", { name: "Refused (0)" })).toBeChecked();
    expect(screen.getByText("No job matches this filter.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("radio", { name: "All (2)" }));
    expect(screen.queryByText("No job matches this filter.")).toBeNull();
  });

  it("works without sessionStorage", async () => {
    const blocked = () => {
      throw new Error("blocked");
    };
    vi.stubGlobal("sessionStorage", { getItem: blocked, setItem: blocked });
    try {
      renderList(mixedList());
      await userEvent.click(screen.getByRole("radio", { name: "Running (1)" }));
      expect(screen.getAllByRole("link")).toHaveLength(1);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("shows a refused job in detail: the reason, the status and the failed checks", async () => {
    const refused: Job = {
      ...runningJob(),
      id: REFUSED_ID,
      kind: "operation",
      operation: "restore",
      state: "failed",
      endedAt: at(10, 0),
      items: [],
      events: [{ seq: 1, time: at(10, 0), type: "job.refused", message: "Restore refused" }],
      refusal: {
        status: 412,
        reason: "pre-flight checks failed",
        checks: [
          { step: "database", label: "Database", status: "failed", detail: "sales exists" },
          { step: "tasks", label: "Native tasks", status: "ok" },
        ],
      },
    };
    mockFetch({ [`GET /api/v1/jobs/${REFUSED_ID}`]: () => json(refused) });
    window.location.hash = `#activity/${REFUSED_ID}`;
    renderI18n(
      <ActivityPage
        engine={ENGINE}
        engineError=""
        jobs={{ jobs: [], error: "", refresh: vi.fn() }}
      />,
    );
    expect(
      await screen.findByRole("heading", { level: 1, name: "Restore on dev-sql" }),
    ).toBeVisible();
    expect(screen.getByText("Refused")).toHaveClass("badge-warning");
    const panel = screen.getByRole("region", { name: "Refused before running" });
    expect(within(panel).getByText("pre-flight checks failed")).toBeVisible();
    expect(panel).toHaveTextContent("HTTP 412 · Precondition failed");
    expect(within(panel).getByText("Database")).toBeVisible();
    expect(within(panel).getByText(/sales exists/)).toBeVisible();
    expect(within(panel).queryByText("Native tasks")).toBeNull();
    // No databases to list, no cancel: the job never ran.
    expect(screen.queryByRole("heading", { name: "Databases" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Cancel job" })).toBeNull();
  });

  it("speaks Italian", async () => {
    renderList(mixedList(), "", "it");
    expect(screen.getByRole("group", { name: "Mostra i job" })).toBeVisible();
    expect(screen.getByRole("radio", { name: "Rifiutati (1)" })).toBeVisible();
    expect(screen.getByText("Rifiutato")).toBeVisible();
    expect(screen.getByText("HTTP 422 · Non confermato")).toBeVisible();
    expect(screen.getByText("Le anteprime e i dry run non vengono registrati qui.")).toBeVisible();
  });
});
