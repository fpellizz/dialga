import { type Mock, vi } from "vitest";
import type { EngineInfo } from "../engine";

/** A fake engine. The token is built, not written: secret scanners stay quiet. */
export const ENGINE: EngineInfo = {
  url: "http://127.0.0.1:43210",
  token: "t".repeat(48),
  version: "0.1.0-test",
};

export function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** An engine error: http.Error answers text/plain with a trailing newline. */
export function text(body: string, status: number): Response {
  return new Response(body, { status, headers: { "Content-Type": "text/plain; charset=utf-8" } });
}

/** A text/event-stream response that the test feeds one event at a time. */
export function sseStream() {
  const encoder = new TextEncoder();
  let controller: ReadableStreamDefaultController<Uint8Array> | undefined;
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      controller = c;
    },
  });
  return {
    response: new Response(body, { headers: { "Content-Type": "text/event-stream" } }),
    send(event: string, data: unknown) {
      controller?.enqueue(encoder.encode(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`));
    },
    close() {
      controller?.close();
    },
  };
}

/** A promise that the test settles when it wants. */
export function deferred<T>() {
  let resolve: (value: T) => void = () => {};
  let reject: (reason: unknown) => void = () => {};
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

export interface FetchRequest {
  url: URL;
  init: RequestInit;
}

/** Answers one route; call is 0 for its first request, 1 for the second... */
export type Route = (request: FetchRequest, call: number) => Response | Promise<Response>;

export type FetchMock = Mock<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>;

/**
 * Replaces fetch with routes keyed by "METHOD /path" (no query string). Like
 * the real fetch, a request rejects with an AbortError when its signal aborts.
 */
export function mockFetch(routes: Record<string, Route>): FetchMock {
  const calls = new Map<string, number>();
  const fetch: FetchMock = vi.fn((input, init = {}) => {
    const url = new URL(String(input));
    const key = `${init.method ?? "GET"} ${url.pathname}`;
    const route = routes[key];
    if (!route) {
      return Promise.reject(new Error(`unexpected request ${key}`));
    }
    const call = calls.get(key) ?? 0;
    calls.set(key, call + 1);
    return new Promise<Response>((resolve, reject) => {
      const abort = () => reject(new DOMException("The operation was aborted.", "AbortError"));
      if (init.signal?.aborted) {
        abort();
        return;
      }
      init.signal?.addEventListener("abort", abort, { once: true });
      Promise.resolve()
        .then(() => route({ url, init }, call))
        .then(resolve, reject);
    });
  });
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

/** The requests made to one route, as "METHOD /path". */
export function requestsTo(fetch: FetchMock, key: string): RequestInit[] {
  return fetch.mock.calls
    .filter(
      ([input, init]) => `${init?.method ?? "GET"} ${new URL(String(input)).pathname}` === key,
    )
    .map(([, init]) => init ?? {});
}
