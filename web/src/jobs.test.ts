import { describe, expect, it } from "vitest";
import {
  applyEvent,
  cancelJob,
  filterCounts,
  followJob,
  formatClock,
  formatDateTime,
  formatDuration,
  getJob,
  HttpError,
  type Job,
  type JobEvent,
  lastSeq,
  listJobs,
  logText,
  matchesFilter,
  recordedRefusal,
  resumeNeeds,
  shownState,
  sortJobs,
} from "./jobs";
import { ENGINE, json, mockFetch, requestsTo, sseStream, text } from "./test/http";
import { at, event, interruptedJob, JOB_ID, runningJob, summary } from "./test/jobFixtures";

const MIB = 1024 * 1024;

describe("applyEvent", () => {
  it("follows a database through its steps, the way the engine does", () => {
    let job = runningJob();
    const upload = {
      direction: "upload" as const,
      bytes: 40 * MIB,
      total: 100 * MIB,
      bytesPerSecond: 10 * MIB,
      etaSeconds: 6,
    };
    job = applyEvent(
      job,
      event(4, "upload.progress", { item: "sales", step: "upload", transfer: upload }),
    );
    expect(job.items[0].transfer).toEqual(upload);
    expect(job.items[0].percent).toBe(40);
    // Progress is live only: the log keeps what the engine keeps.
    expect(job.events).toHaveLength(3);

    job = applyEvent(
      job,
      event(5, "step.succeeded", { item: "sales", step: "upload", message: "100 MiB" }),
    );
    expect(job.items[0].steps[0]).toMatchObject({
      status: "succeeded",
      detail: "100 MiB",
      endedAt: at(11, 2, 5),
    });
    expect(job.items[0].state).toBe("running"); // restore still to do
    expect(job.items[0].percent).toBe(100);

    job = applyEvent(job, event(6, "step.waiting", { item: "sales", step: "restore" }));
    expect(job.items[0]).toMatchObject({ step: "restore" });
    expect(job.items[0].steps[1].status).toBe("waiting");

    job = applyEvent(job, event(7, "step.started", { item: "sales", step: "restore" }));
    expect(job.items[0]).toMatchObject({ step: "restore", percent: 0, transfer: undefined });
    const created = { id: 7, type: "RESTORE_DB", lifecycle: "CREATED" as const, percent: 0 };
    job = applyEvent(
      job,
      event(8, "task.status", { item: "sales", step: "restore", task: created }),
    );
    job = applyEvent(
      job,
      event(9, "task.status", {
        item: "sales",
        step: "restore",
        task: { ...created, lifecycle: "IN_PROGRESS", percent: 10 },
      }),
    );
    job = applyEvent(
      job,
      event(10, "task.status", {
        item: "sales",
        step: "restore",
        task: { ...created, lifecycle: "IN_PROGRESS", percent: 55 },
      }),
    );
    expect(job.items[0].task?.percent).toBe(55);
    expect(job.items[0].percent).toBe(55);
    expect(job.items[0].taskIds).toEqual([7]);
    // Logged: a new task and a new lifecycle; not the percentage alone.
    expect(job.events.filter((e) => e.type === "task.status").map((e) => e.seq)).toEqual([8, 9]);

    job = applyEvent(job, event(11, "step.succeeded", { item: "sales", step: "restore" }));
    expect(job.items[0].state).toBe("succeeded");

    job = applyEvent(
      job,
      event(12, "step.failed", { item: "hr", step: "upload", message: "Access Denied" }),
    );
    job = applyEvent(job, event(13, "step.skipped", { item: "hr", step: "restore" }));
    expect(job.items[1]).toMatchObject({ state: "failed", error: "Access Denied" });
    expect(job.items[1].steps.map((s) => s.status)).toEqual(["failed", "skipped"]);
    expect(job.items[1].steps[0].detail).toBe("Access Denied");

    job = applyEvent(job, event(14, "job.failed", { message: "1 of 2 database(s) failed." }));
    expect(job.state).toBe("failed");
    expect(job.endedAt).toBe(at(11, 2, 14));
    expect(job.updatedAt).toBe(at(11, 2, 14));
  });

  it("records the cancel and the stopped databases", () => {
    let job = runningJob();
    job = applyEvent(job, event(4, "job.cancelling"));
    expect(job.cancelRequested).toBe(true);
    job = applyEvent(job, event(5, "step.cancelled", { item: "sales", step: "upload" }));
    job = applyEvent(job, event(6, "step.interrupted", { item: "hr", step: "upload" }));
    expect(job.items.map((i) => i.state)).toEqual(["cancelled", "interrupted"]);
    expect(job.items[0].steps[0].endedAt).toBe(at(11, 2, 5));
    job = applyEvent(job, event(7, "job.cancelled"));
    expect(job.state).toBe("cancelled");
  });

  it("starts a queued job with its first step and ignores unknown databases", () => {
    let job: Job = { ...runningJob(), state: "queued" };
    job = applyEvent(job, event(4, "step.started", { item: "hr", step: "upload" }));
    expect(job.state).toBe("running");
    expect(job.items[1].state).toBe("running");
    const before = job;
    job = applyEvent(job, event(5, "step.started", { item: "nope", step: "upload" }));
    expect(job.items).toBe(before.items);
    job = applyEvent(job, event(6, "log", { message: "hello", level: "info" }));
    expect(job.events.at(-1)?.message).toBe("hello");
  });

  it("never changes the job it is given", () => {
    const job = runningJob();
    const copy = structuredClone(job);
    applyEvent(job, event(4, "step.succeeded", { item: "sales", step: "upload" }));
    applyEvent(job, event(5, "job.succeeded"));
    expect(job).toEqual(copy);
  });
});

describe("job helpers", () => {
  it("sorts the active jobs first, newest first in each group", () => {
    const base = summary(runningJob());
    const list = [
      { ...base, id: "a", state: "succeeded" as const, createdAt: at(12, 0) },
      { ...base, id: "b", state: "running" as const, createdAt: at(9, 0) },
      { ...base, id: "c", state: "failed" as const, createdAt: at(13, 0) },
      { ...base, id: "d", state: "queued" as const, createdAt: at(10, 0) },
    ];
    expect(sortJobs(list).map((j) => j.id)).toEqual(["d", "b", "c", "a"]);
    expect(list[0].id).toBe("a"); // a copy
  });

  it("knows where the event stream resumes", () => {
    expect(lastSeq(runningJob())).toBe(3);
    expect(lastSeq({ ...runningJob(), events: [] })).toBe(0);
  });

  it("collects what a resume needs", () => {
    expect(resumeNeeds(interruptedJob())).toEqual([
      {
        item: "sales",
        lastStep: "Upload sales.bak",
        pending: ["Upload sales.bak", "Restore sales"],
        upload: interruptedJob().items[0].upload,
        partsTotal: 10,
        taskIds: [],
        taskLeftRunning: false,
      },
      {
        item: "hr",
        lastStep: "Restore hr",
        pending: ["Restore hr"],
        upload: undefined,
        partsTotal: undefined,
        taskIds: [12],
        taskLeftRunning: true,
      },
    ]);
  });

  it("formats times in the local zone and durations", () => {
    expect(formatDateTime(at(9, 5))).toBe("2026-10-16 09:05");
    expect(formatDateTime(undefined)).toBe("");
    expect(formatDateTime("not a time")).toBe("not a time");
    expect(formatClock(at(9, 5, 7))).toBe("09:05:07");
    expect(formatDuration(0)).toBe("0 s");
    expect(formatDuration(44.6)).toBe("45 s");
    expect(formatDuration(600)).toBe("10 min");
    expect(formatDuration(3570)).toBe("1 h 00 min");
    expect(formatDuration(7170)).toBe("2 h 00 min");
    expect(formatDuration(3900)).toBe("1 h 05 min");
  });

  it("writes the log as plain text", () => {
    const lines = logText(interruptedJob()).split("\n");
    expect(lines[0]).toBe(
      `Dialga job ${JOB_ID}: import on dev-sql (instance my-sqlserver), interrupted`,
    );
    expect(lines[1]).toBe(`Created ${at(11, 0)}, ended ${at(11, 42)}`);
    expect(lines[3]).toBe(`${at(11, 0)}  job.queued  import of 2 database(s) on dev-sql`);
    expect(lines[5]).toBe(`${at(11, 1)}  step.started  sales/upload  Upload sales.bak`);
    expect(lines[6]).toBe(
      `${at(11, 42)}  log  hr  WARN  Native task 12 (IN_PROGRESS) is still running on the instance`,
    );
    expect(lines.at(-1)).toBe("");
  });
});

describe("jobs API", () => {
  it("lists, reads and cancels with the session token", async () => {
    const job = runningJob();
    const fetch = mockFetch({
      "GET /api/v1/jobs": () => json({ jobs: [summary(job)] }),
      [`GET /api/v1/jobs/${JOB_ID}`]: () => json(job),
      [`POST /api/v1/jobs/${JOB_ID}/cancel`]: () =>
        json(summary(job, { cancelRequested: true }), 202),
    });
    expect((await listJobs(ENGINE)).map((j) => j.id)).toEqual([JOB_ID]);
    expect((await getJob(ENGINE, JOB_ID)).items).toHaveLength(2);
    expect((await cancelJob(ENGINE, JOB_ID)).cancelRequested).toBe(true);
    for (const [, init] of fetch.mock.calls) {
      expect(init?.headers).toMatchObject({ Authorization: `Bearer ${ENGINE.token}` });
    }
  });

  it("turns engine errors into HttpError with the status", async () => {
    mockFetch({
      [`GET /api/v1/jobs/${JOB_ID}`]: () => text("job not found\n", 404),
      [`POST /api/v1/jobs/${JOB_ID}/cancel`]: () => text("job already ended (succeeded)\n", 409),
      "GET /api/v1/jobs": () => text("", 500),
    });
    await expect(getJob(ENGINE, JOB_ID)).rejects.toMatchObject({
      status: 404,
      message: "job not found",
    });
    const error = await cancelJob(ENGINE, JOB_ID).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(HttpError);
    expect(error).toMatchObject({ status: 409, message: "job already ended (succeeded)" });
    await expect(listJobs(ENGINE)).rejects.toMatchObject({ status: 500, message: "HTTP 500" });
  });

  it("follows the events after Last-Event-ID until the final one", async () => {
    const stream = sseStream();
    const fetch = mockFetch({ [`GET /api/v1/jobs/${JOB_ID}/events`]: () => stream.response });
    const got: JobEvent[] = [];
    const done = followJob(ENGINE, JOB_ID, 3, (e) => got.push(e));
    stream.send("step.succeeded", event(4, "step.succeeded", { item: "sales", step: "upload" }));
    stream.send("job.succeeded", event(5, "job.succeeded"));
    stream.close();
    expect(await done).toBe(true);
    expect(got.map((e) => e.seq)).toEqual([4, 5]);
    expect(requestsTo(fetch, `GET /api/v1/jobs/${JOB_ID}/events`)[0].headers).toEqual({
      Authorization: `Bearer ${ENGINE.token}`,
      "Last-Event-ID": "3",
    });
  });

  it("says when the stream closed before the end, and asks from the start with 0", async () => {
    const stream = sseStream();
    const fetch = mockFetch({ [`GET /api/v1/jobs/${JOB_ID}/events`]: () => stream.response });
    const done = followJob(ENGINE, JOB_ID, 0, () => {});
    stream.send("log", event(4, "log"));
    stream.close();
    expect(await done).toBe(false);
    expect(requestsTo(fetch, `GET /api/v1/jobs/${JOB_ID}/events`)[0].headers).toEqual({
      Authorization: `Bearer ${ENGINE.token}`,
    });
  });
});

describe("filters and refusals", () => {
  const base = summary(runningJob());
  const list = [
    base,
    { ...base, id: "f", state: "failed" as const },
    { ...base, id: "r", state: "failed" as const, refusal: { status: 412, reason: "x" } },
    { ...base, id: "s", state: "succeeded" as const },
  ];

  it("counts a refused job as Refused, not as Failed", () => {
    expect(filterCounts(list)).toEqual({ all: 4, running: 1, failed: 1, refused: 1 });
    expect(list.filter((j) => matchesFilter(j, "refused")).map((j) => j.id)).toEqual(["r"]);
    expect(shownState(list[2])).toBe("refused");
    expect(shownState(list[1])).toBe("failed");
  });

  it("knows the refusals the engine records", () => {
    expect(recordedRefusal({ status: 422 })).toBe(true);
    expect(recordedRefusal({ status: 428 })).toBe(true);
    expect(recordedRefusal({ status: 404 })).toBe(false);
    expect(recordedRefusal(new Error("x"))).toBe(false);
    expect(recordedRefusal(null)).toBe(false);
  });
});
