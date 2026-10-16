import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import {
  canExecute,
  cleanRequest,
  executePlan,
  getDatabaseReport,
  planHint,
  planStepKey,
  previewPlan,
  s3URI,
  sameRequest,
  upsertStep,
} from "./plans";
import { ENGINE, json, mockFetch, requestsTo, sseStream, text } from "./test/http";
import { DROP_PREVIEW } from "./test/plans";

const PREVIEW = "POST /api/v1/targets/dev-sql/plans/drop";
const EXECUTE = "POST /api/v1/targets/dev-sql/plans/drop/execute";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("plans API", () => {
  it("previews with a JSON body and the session token", async () => {
    const fetch = mockFetch({ [PREVIEW]: () => json(DROP_PREVIEW) });
    const got = await previewPlan(ENGINE, "dev-sql", "drop", { database: "sales" });
    expect(got).toEqual(DROP_PREVIEW);
    const [init] = requestsTo(fetch, PREVIEW);
    expect(init.body).toBe('{"database":"sales"}');
    expect(init.headers).toEqual({
      Authorization: `Bearer ${ENGINE.token}`,
      "Content-Type": "application/json",
    });
  });

  it("keeps the HTTP status of an engine error", async () => {
    mockFetch({ [PREVIEW]: () => text("SQL password: not set\n", 422) });
    const error = await previewPlan(ENGINE, "dev-sql", "drop", {}).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status: 422, message: "SQL password: not set" });
  });

  it("streams an execution until done", async () => {
    const stream = sseStream();
    mockFetch({ [EXECUTE]: () => stream.response });
    const events: string[] = [];
    const running = executePlan(
      ENGINE,
      "dev-sql",
      "drop",
      { database: "sales", confirm: "sales", digest: "x" },
      {
        onNotice: (m) => events.push(`notice ${m}`),
        onStep: (e) => events.push(`step ${e.step} ${e.status}`),
        onTask: (t) => events.push(`task ${t.lifecycle}`),
      },
    );
    stream.send("notice", { message: "Native tasks: 1 running" });
    stream.send("step", { step: "drop", label: "Drop database sales", status: "running" });
    stream.send("task", { id: 3, type: "X", database: "sales", percent: 0, lifecycle: "CREATED" });
    stream.send("step", { step: "drop", label: "Drop database sales", status: "ok" });
    stream.send("done", { ok: true });
    stream.close();
    expect(await running).toEqual({ ok: true });
    expect(events).toEqual([
      "notice Native tasks: 1 running",
      "step drop running",
      "task CREATED",
      "step drop ok",
    ]);
  });

  it("fails a stream that ends without done, and a refused execution", async () => {
    const stream = sseStream();
    mockFetch({ [EXECUTE]: (_r, call) => (call === 0 ? stream.response : text("changed\n", 412)) });
    const handlers = { onNotice() {}, onStep() {}, onTask() {} };
    const cut = executePlan(ENGINE, "dev-sql", "drop", {}, handlers);
    stream.close();
    await expect(cut).rejects.toThrow("the engine closed the stream before the end");
    await expect(executePlan(ENGINE, "dev-sql", "drop", {}, handlers)).rejects.toMatchObject({
      status: 412,
    });
  });

  it("reads the report of a database", async () => {
    const fetch = mockFetch({
      "GET /api/v1/targets/dev-sql/databases/sales/report": () => json({ ok: true }),
    });
    expect(await getDatabaseReport(ENGINE, "dev-sql", "sales")).toEqual({ ok: true });
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});

describe("plan helpers", () => {
  it("runs only a ready, fresh and confirmed plan", () => {
    expect(canExecute(DROP_PREVIEW, "sales", false)).toBe(true);
    expect(canExecute(DROP_PREVIEW, "Sales", false)).toBe(false);
    expect(canExecute(DROP_PREVIEW, "sales ", false)).toBe(false);
    expect(canExecute(DROP_PREVIEW, "sales", true)).toBe(false);
    expect(canExecute({ ...DROP_PREVIEW, ready: false }, "sales", false)).toBe(false);
    expect(canExecute({ ...DROP_PREVIEW, confirmation: undefined }, "", false)).toBe(true);
  });

  it("compares requests without blanks and empty fields", () => {
    expect(cleanRequest({ database: " sales ", key: "", differential: false })).toEqual({
      database: "sales",
    });
    expect(
      sameRequest({ database: "sales", key: " a.bak" }, { key: "a.bak", database: "sales" }),
    ).toBe(true);
    expect(sameRequest({ database: "sales" }, { database: "sales", noRecovery: true })).toBe(false);
  });

  it("gives a hint for the refusals of a plan, after those of the message", () => {
    expect(planHint(new ApiError(400, ""))).toEqual({ key: "plan.hint.400" });
    expect(planHint(new ApiError(412, ""))).toEqual({ key: "plan.hint.412" });
    expect(planHint(new ApiError(422, ""))).toEqual({ key: "plan.hint.422" });
    expect(planHint(new ApiError(502, ""))).toEqual({ key: "plan.hint.502" });
    // 404 and 409 are hints.ts's (unknown target, configuration).
    expect(planHint(new ApiError(409, ""))).toBeUndefined();
    expect(planHint(new ApiError(500, ""))).toBeUndefined();
    expect(planHint(new TypeError("Failed to fetch"))).toEqual({ key: "plan.hint.network" });
    expect(planHint("not an error")).toBeUndefined();
  });

  it("maps step ids, ARNs and step updates", () => {
    expect(planStepKey("source")).toBe("plancheck.source");
    expect(planStepKey("orphans")).toBe("report.finding.orphans");
    expect(planStepKey("aws")).toBeUndefined();
    expect(s3URI("arn:aws:s3:::bucket/export/a.bak")).toBe("s3://bucket/export/a.bak");
    expect(s3URI(undefined)).toBe("");
    const running = { step: "drop", label: "Drop", status: "running" as const };
    const steps = upsertStep([running], { ...running, status: "ok" });
    expect(steps).toEqual([{ ...running, status: "ok" }]);
    expect(upsertStep(steps, { step: "wait", label: "Wait", status: "running" })).toHaveLength(2);
  });
});
