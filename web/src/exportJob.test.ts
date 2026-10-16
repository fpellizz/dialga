import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { previewExport, resetBackends, runExport } from "./exportBackend";
import { type Job, type JobPlan, mapJob, NoJobsError, previewJob, startJob } from "./exportJob";
import { HttpError } from "./plans";
import { ENGINE, json, mockFetch, requestsTo, text } from "./test/http";

const JOB_ID = "20261016T090000Z-0a1b2c3d";
const KEY = "export/sales_20261016-090000.bak";
const FILE = "/home/u/backups/sales_20261016-090000.bak";

const PLAN: JobPlan = {
  kind: "export",
  target: "dev-sql",
  instance: "my-sqlserver",
  protected: false,
  ready: true,
  digest: "j".repeat(32),
  items: [
    {
      database: "sales",
      file: FILE,
      key: KEY,
      ready: true,
      checks: [
        {
          step: "destination",
          label: "Destination in S3",
          status: "ok",
          detail: `s3://my-sqlserver-backups/${KEY} is free`,
        },
      ],
      actions: [
        { id: "backup", title: "Back up sales", statement: "exec msdb.dbo.rds_backup_database …" },
      ],
    },
  ],
  preview: "Dry run: export of 1 database on dev-sql",
};

function job(state: string, item: Partial<Job["items"][number]>): Job {
  return {
    id: JOB_ID,
    kind: "export",
    target: "dev-sql",
    state,
    items: [{ name: "sales", state, percent: 0, steps: [], ...item }],
  };
}

const RUNNING = job("running", {
  step: "wait",
  percent: 40,
  steps: [
    { id: "backup", title: "Back up sales", status: "succeeded", detail: "task 21 created" },
    { id: "wait", title: "Wait for the backup task", status: "running" },
    { id: "download", title: `Download ${KEY}`, status: "pending" },
  ],
  task: { id: 21, type: "BACKUP_DB", lifecycle: "IN_PROGRESS", percent: 40 },
});

const DONE = job("succeeded", {
  step: "download",
  percent: 100,
  steps: [
    { id: "backup", title: "Back up sales", status: "succeeded" },
    { id: "wait", title: "Wait for the backup task", status: "succeeded" },
    {
      id: "download",
      title: `Download ${KEY}`,
      status: "succeeded",
      detail: "6 GiB, SHA256 verified",
    },
  ],
  task: { id: 21, type: "BACKUP_DB", lifecycle: "SUCCESS", percent: 100 },
  result: `sales exported to ${FILE}`,
});

beforeEach(() => resetBackends());
afterEach(() => vi.unstubAllGlobals());

describe("previewJob", () => {
  it("tells an engine without the job route from a real 404", async () => {
    mockFetch({ "POST /api/v1/jobs": () => text("Method Not Allowed\n", 405) });
    await expect(previewJob(ENGINE, "dev-sql", [{ database: "sales" }])).rejects.toBeInstanceOf(
      NoJobsError,
    );
    mockFetch({ "POST /api/v1/jobs": () => text('unknown target "x" (known: dev-sql)\n', 404) });
    const error = await previewJob(ENGINE, "x", [{ database: "sales" }]).catch((e) => e);
    expect(error).toBeInstanceOf(HttpError);
    expect(error).toMatchObject({ status: 404 });
  });
});

describe("previewExport", () => {
  it("previews one job and starts what it previewed", async () => {
    const fetch = mockFetch({
      "POST /api/v1/jobs": (_, call) => (call === 0 ? json(PLAN) : json(RUNNING, 201)),
      [`GET /api/v1/jobs/${JOB_ID}/events`]: () =>
        new Response(
          "id: 1\nevent: job.started\ndata: {}\n\nid: 2\nevent: job.succeeded\ndata: {}\n\n",
          { headers: { "Content-Type": "text/event-stream" } },
        ),
      [`GET /api/v1/jobs/${JOB_ID}`]: () => json(DONE),
    });
    const preview = await previewExport(
      ENGINE,
      "dev-sql",
      [{ database: "sales", key: "" }],
      { folder: "/home/u/backups", overwrite: false },
      "my-sqlserver-backups",
    );
    expect(preview.via).toBe("job");
    expect(preview.items[0]).toMatchObject({
      database: "sales",
      key: KEY,
      s3Uri: `s3://my-sqlserver-backups/${KEY}`,
      localFile: FILE,
      ready: true,
    });
    const [dry, start] = requestsTo(fetch, "POST /api/v1/jobs").map((r) =>
      JSON.parse(String(r.body)),
    );
    expect(dry).toEqual({
      kind: "export",
      target: "dev-sql",
      items: [{ database: "sales", destination: "/home/u/backups", overwrite: false }],
      dryRun: true,
    });

    const ids: string[] = [];
    const items = await runExport(
      ENGINE,
      preview,
      undefined,
      () => {},
      new AbortController().signal,
      (id) => ids.push(id),
    );
    expect(ids).toEqual([JOB_ID]);
    const body = JSON.parse(String(requestsTo(fetch, "POST /api/v1/jobs")[1]?.body));
    expect(body).toEqual({
      kind: "export",
      target: "dev-sql",
      items: [{ database: "sales", key: KEY, destination: FILE, overwrite: false }],
      digest: PLAN.digest,
    });
    expect(start).toBeUndefined();
    expect(items[0]).toMatchObject({
      state: "succeeded",
      s3Uri: `s3://my-sqlserver-backups/${KEY}`,
      localFile: FILE,
      note: "6 GiB, SHA256 verified",
    });
  });

  it("falls back on the plan calls once, and remembers it", async () => {
    const fetch = mockFetch({
      "POST /api/v1/jobs": () => text("Method Not Allowed\n", 405),
      "POST /api/v1/targets/dev-sql/plans/export": () =>
        json({
          plan: {
            operation: "export",
            target: "dev-sql",
            instance: "i",
            protected: false,
            actions: [],
          },
          checks: [],
          ready: true,
          digest: "d".repeat(32),
          preview: "",
          request: { database: "sales", key: KEY },
        }),
    });
    expect((await previewExport(ENGINE, "dev-sql", [{ database: "sales", key: "" }])).via).toBe(
      "plans",
    );
    expect((await previewExport(ENGINE, "dev-sql", [{ database: "sales", key: "" }])).via).toBe(
      "plans",
    );
    expect(requestsTo(fetch, "POST /api/v1/jobs")).toHaveLength(1);
  });

  it("does not fall back when the job route refuses for a reason", async () => {
    mockFetch({
      "POST /api/v1/jobs": () => text("SQL password: environment variable X is empty\n", 422),
    });
    await expect(
      previewExport(ENGINE, "dev-sql", [{ database: "sales", key: "" }]),
    ).rejects.toMatchObject({ status: 422 });
  });
});

describe("startJob", () => {
  it("reads the reason of a 412 refusal", async () => {
    mockFetch({
      "POST /api/v1/jobs": () =>
        json({ reason: "the plan changed since the preview: preview it again", plan: PLAN }, 412),
    });
    const error = await startJob(ENGINE, "dev-sql", [{ database: "sales" }], PLAN).catch((e) => e);
    expect(error).toBeInstanceOf(HttpError);
    expect(error).toMatchObject({
      status: 412,
      message: "the plan changed since the preview: preview it again",
    });
  });
});

describe("mapJob", () => {
  it("maps a running item: steps, task, no result yet", () => {
    const [item] = mapJob(RUNNING, PLAN);
    expect(item).toMatchObject({ database: "sales", key: KEY, state: "running", step: "backup" });
    expect(item?.steps.map((s) => [s.step, s.status])).toEqual([
      ["backup", "ok"],
      ["wait", "running"],
    ]);
    expect(item?.task).toMatchObject({
      id: 21,
      lifecycle: "IN_PROGRESS",
      percent: 40,
      database: "sales",
    });
    expect(item?.localFile).toBeUndefined();
  });

  it("maps the download progress and the failures", () => {
    const downloading = job("running", {
      step: "download",
      steps: [{ id: "download", title: "Download", status: "running" }],
      transfer: { direction: "download", bytes: 5, total: 10, bytesPerSecond: 1 },
    });
    expect(mapJob(downloading, PLAN, "my-sqlserver-backups")[0]).toMatchObject({
      step: "download",
      transfer: { done: 5, total: 10 },
      s3Uri: `s3://my-sqlserver-backups/${KEY}`,
    });
    const failed = job("interrupted", { error: "the engine stopped", taskLeftRunning: true });
    expect(mapJob(failed, PLAN)[0]).toMatchObject({ state: "failed", taskLeftRunning: true });
    expect(String(mapJob(failed, PLAN)[0]?.error)).toContain("the engine stopped");
  });
});
