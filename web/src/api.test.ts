import { afterEach, describe, expect, it, vi } from "vitest";
import {
  checkTarget,
  errorText,
  HttpError,
  listTargets,
  listTasks,
  type StepEvent,
} from "./api";

const ENGINE = { url: "http://127.0.0.1:43210", token: "t".repeat(48), version: "0.1.0" };
const AUTH = { Authorization: `Bearer ${ENGINE.token}` };

const ignore = () => {};

function stubFetch(response: () => Response) {
  const fetch = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => response());
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

/** A text/event-stream body delivered in the given chunks. */
function sse(chunks: string[]): Response {
  const encoder = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) {
        controller.enqueue(encoder.encode(chunk));
      }
      controller.close();
    },
  });
  return new Response(body, { headers: { "Content-Type": "text/event-stream" } });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("api", () => {
  it("lists the targets with the session token", async () => {
    const fetch = stubFetch(() => Response.json({ configPath: "/c.yaml", targets: [] }));
    await expect(listTargets(ENGINE)).resolves.toEqual({ configPath: "/c.yaml", targets: [] });
    expect(fetch).toHaveBeenCalledWith(`${ENGINE.url}/api/v1/targets`, { headers: AUTH });
  });

  it.each([
    ["the engine message", 'unknown target "x"\n', 404, 'unknown target "x"'],
    ["the status without a message", "", 502, "HTTP 502"],
  ])("fails with %s", async (_name, body, status, message) => {
    stubFetch(() => new Response(body, { status }));
    // The message is the engine text as is; the status comes along.
    await expect(listTargets(ENGINE)).rejects.toThrow(new HttpError(message, status));
  });

  it("lists the tasks of an escaped target name", async () => {
    const fetch = stubFetch(() => Response.json({ tasks: [] }));
    const signal = new AbortController().signal;
    await expect(listTasks(ENGINE, "a b/c", signal)).resolves.toEqual([]);
    expect(fetch).toHaveBeenCalledWith(`${ENGINE.url}/api/v1/targets/a%20b%2Fc/tasks`, {
      headers: AUTH,
      signal,
    });
  });

  it("streams a check event by event, across chunk boundaries", async () => {
    const fetch = stubFetch(() =>
      sse([
        'event: notice\ndata: {"message":"SQL login will be skipped"}\n\n',
        'event: step\ndata: {"step":"aws","label":"AWS credentials","sta',
        'tus":"running"}\n\nevent: step\ndata: {"step":"aws","label":"AWS credentials","status":"ok"}\n\n',
        ': comment\n\nevent: done\ndata: {"ok":true}\n\n',
      ]),
    );
    const steps: StepEvent[] = [];
    const notices: string[] = [];
    const passed = await checkTarget(
      ENGINE,
      "dev-sql",
      (s) => steps.push(s),
      (n) => notices.push(n),
    );
    expect(passed).toBe(true);
    expect(notices).toEqual(["SQL login will be skipped"]);
    expect(steps.map((s) => s.status)).toEqual(["running", "ok"]);
    expect(fetch).toHaveBeenCalledWith(`${ENGINE.url}/api/v1/targets/dev-sql/checks`, {
      method: "POST",
      headers: AUTH,
      signal: undefined,
    });
  });

  it("treats a stream that ends without a verdict as failed", async () => {
    stubFetch(() =>
      sse(['event: step\ndata: {"step":"aws","label":"AWS","status":"running"}\n\n']),
    );
    await expect(checkTarget(ENGINE, "dev-sql", ignore, ignore)).resolves.toBe(false);
  });

  it("fails a refused check with the engine message", async () => {
    stubFetch(() => new Response("connect.mode tunnel arrives in v0.4\n", { status: 422 }));
    await expect(checkTarget(ENGINE, "dev-sql", ignore, ignore)).rejects.toThrow(
      new HttpError("connect.mode tunnel arrives in v0.4", 422),
    );
  });

  it("drops the Error: prefix from failure messages", () => {
    expect(errorText(new TypeError("Failed to fetch"))).toBe("Failed to fetch");
    expect(errorText("plain")).toBe("plain");
  });
});
