import { afterEach, describe, expect, it, vi } from "vitest";
import type { Task } from "./api";
import { type ExportItemRun, previewURI, queuedItems, runExports, tally } from "./exportRun";
import type { PlanPreview } from "./plans";
import { ENGINE, mockFetch, requestsTo, text } from "./test/http";

afterEach(() => {
  vi.unstubAllGlobals();
});

function preview(database: string, protectedTarget = false): PlanPreview {
  const key = `export/${database}_20261016-080000.bak`;
  return {
    plan: {
      operation: "export",
      target: "dev-sql",
      instance: "my-sqlserver",
      protected: protectedTarget,
      subject: database,
      actions: [
        {
          id: "export",
          title: `Back up ${database} to s3://my-sqlserver-backups/${key}`,
          kind: "sql",
          risk: "change",
          statement: "exec msdb.dbo.rds_backup_database …",
        },
      ],
    },
    checks: [
      {
        step: "destination",
        label: "Destination in S3",
        status: "ok",
        detail: `s3://my-sqlserver-backups/${key} is free`,
      },
    ],
    ready: true,
    confirmation: protectedTarget ? "dev-sql" : undefined,
    digest: `${database[0]}`.repeat(32),
    preview: "Dry run",
    request: { database, key },
  };
}

function task(database: string, lifecycle: Task["lifecycle"]): Task {
  return {
    id: database === "sales" ? 11 : 12,
    type: "BACKUP_DB",
    database,
    percent: lifecycle === "SUCCESS" ? 100 : 40,
    lifecycle,
    s3Object: `arn:aws:s3:::my-sqlserver-backups/export/${database}_20261016-080000.bak`,
  };
}

/** A whole event stream in one response, as the engine writes it. */
function sse(events: [string, unknown][]): Response {
  const body = events.map(([e, d]) => `event: ${e}\ndata: ${JSON.stringify(d)}\n\n`).join("");
  return new Response(body, { headers: { "Content-Type": "text/event-stream" } });
}

const EXECUTE = "POST /api/v1/targets/dev-sql/plans/export/execute";
const DOWNLOAD = "POST /api/v1/targets/dev-sql/downloads";

describe("runExports", () => {
  it("backs up and downloads each database in turn", async () => {
    const fetch = mockFetch({
      [EXECUTE]: (_, call) => {
        const db = call === 0 ? "sales" : "crm";
        return sse([
          ["step", { step: "export", label: "Back up", status: "ok", detail: "task created" }],
          ["task", task(db, "IN_PROGRESS")],
          ["task", task(db, "SUCCESS")],
          ["done", { ok: true, task: task(db, "SUCCESS") }],
        ]);
      },
      [DOWNLOAD]: (_, call) => {
        const db = call === 0 ? "sales" : "crm";
        return sse([
          ["plan", { confirmation: "", items: [], rejected: [] }],
          ["progress", { path: "", key: "", done: 512, total: 1024, bytesPerSecond: 100 }],
          [
            "done",
            {
              ok: true,
              results: [
                {
                  path: `/home/u/backups/${db}_20261016-080000.bak`,
                  bucket: "my-sqlserver-backups",
                  key: `export/${db}_20261016-080000.bak`,
                  size: 1024,
                  checksumAlgorithm: "SHA256",
                  checksum: "q1w2e3r4t5y6u7i8o9p0==",
                  verified: true,
                  seconds: 1,
                  bytesPerSecond: 1024,
                },
              ],
            },
          ],
        ]);
      },
    });
    const seen: ExportItemRun[][] = [];
    const items = await runExports(
      ENGINE,
      {
        target: "dev-sql",
        previews: [preview("sales"), preview("crm")],
        download: { folder: "/home/u/backups", overwrite: false },
      },
      (next) => seen.push(next),
      new AbortController().signal,
    );
    expect(items.map((i) => i.state)).toEqual(["succeeded", "succeeded"]);
    expect(items[0]?.s3Uri).toBe("s3://my-sqlserver-backups/export/sales_20261016-080000.bak");
    expect(items[1]?.download?.path).toBe("/home/u/backups/crm_20261016-080000.bak");
    expect(items[0]?.task?.lifecycle).toBe("SUCCESS");
    // Executes the previewed request with its digest; downloads to the file, not the folder.
    const [first] = requestsTo(fetch, EXECUTE);
    expect(JSON.parse(String(first?.body))).toEqual({
      database: "sales",
      key: "export/sales_20261016-080000.bak",
      digest: "s".repeat(32),
    });
    expect(JSON.parse(String(requestsTo(fetch, DOWNLOAD)[0]?.body))).toEqual({
      key: "export/sales_20261016-080000.bak",
      dest: "/home/u/backups/sales_20261016-080000.bak",
      overwrite: false,
      apply: true,
    });
    // Seen in order: sales downloading while crm still waits.
    expect(
      seen.some(
        (s) => s[0]?.step === "download" && s[0].state === "running" && s[1]?.state === "queued",
      ),
    ).toBe(true);
    expect(seen.some((s) => s[0]?.transfer?.done === 512)).toBe(true);
    expect(tally(items)).toEqual({ succeeded: 2, failed: 0, cancelled: 0, running: 0 });
  });

  it("goes on after a failed backup and says why", async () => {
    mockFetch({
      [EXECUTE]: (_, call) =>
        call === 0
          ? sse([["done", { ok: false, error: "exec: Msg 3201", task: task("sales", "ERROR") }]])
          : text("SQL password: environment variable X is empty\n", 422),
    });
    const items = await runExports(
      ENGINE,
      { target: "dev-sql", previews: [preview("sales"), preview("crm")] },
      () => {},
      new AbortController().signal,
    );
    expect(items.map((i) => i.state)).toEqual(["failed", "failed"]);
    expect(String(items[0]?.error)).toContain("Msg 3201");
    expect(items[1]?.error).toMatchObject({ status: 422 });
  });

  it("refuses an existing local file unless asked to overwrite", async () => {
    mockFetch({
      [EXECUTE]: () => sse([["done", { ok: true, task: task("sales", "SUCCESS") }]]),
      [DOWNLOAD]: () =>
        sse([
          [
            "plan",
            {
              confirmation: "",
              items: [],
              rejected: [{ path: "/b/sales.bak", reason: "/b/sales.bak already exists (1 KiB)" }],
            },
          ],
          ["done", { ok: false, results: [] }],
        ]),
    });
    const [item] = await runExports(
      ENGINE,
      {
        target: "dev-sql",
        previews: [preview("sales")],
        download: { folder: "/b", overwrite: false },
      },
      () => {},
      new AbortController().signal,
    );
    expect(item?.state).toBe("failed");
    expect(String(item?.error)).toContain("already exists");
    // The backup is in S3 all the same.
    expect(item?.s3Uri).toContain("s3://my-sqlserver-backups/");
  });

  it("sends the confirmation of a protected target to both calls", async () => {
    const fetch = mockFetch({
      [EXECUTE]: () => sse([["done", { ok: true, task: task("sales", "SUCCESS") }]]),
      [DOWNLOAD]: () =>
        sse([
          [
            "done",
            {
              ok: true,
              results: [
                {
                  path: "/b/x",
                  bucket: "b",
                  key: "k",
                  size: 1,
                  verified: false,
                  seconds: 0,
                  bytesPerSecond: 0,
                },
              ],
            },
          ],
        ]),
    });
    await runExports(
      ENGINE,
      {
        target: "dev-sql",
        previews: [preview("sales", true)],
        confirm: "dev-sql",
        download: { folder: "/b", overwrite: true },
      },
      () => {},
      new AbortController().signal,
    );
    expect(JSON.parse(String(requestsTo(fetch, EXECUTE)[0]?.body)).confirm).toBe("dev-sql");
    expect(JSON.parse(String(requestsTo(fetch, DOWNLOAD)[0]?.body))).toMatchObject({
      confirm: "dev-sql",
      overwrite: true,
    });
  });

  it("stops following when aborted: the rest is not run", async () => {
    const controller = new AbortController();
    mockFetch({
      [EXECUTE]: () => {
        controller.abort();
        return sse([["done", { ok: true, task: task("sales", "SUCCESS") }]]);
      },
    });
    const items = await runExports(
      ENGINE,
      { target: "dev-sql", previews: [preview("sales"), preview("crm")] },
      () => {},
      controller.signal,
    );
    expect(items.map((i) => i.state)).toEqual(["cancelled", "cancelled"]);
    expect(items[0]?.error).toBeUndefined();
  });
});

describe("helpers", () => {
  it("starts every database queued with its resolved key", () => {
    expect(queuedItems({ target: "dev-sql", previews: [preview("sales")] })).toEqual([
      { database: "sales", key: "export/sales_20261016-080000.bak", state: "queued", steps: [] },
    ]);
  });

  it("reads the S3 URI of a preview", () => {
    expect(previewURI(preview("sales"))).toBe(
      "s3://my-sqlserver-backups/export/sales_20261016-080000.bak",
    );
  });
});
