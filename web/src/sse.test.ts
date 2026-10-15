import { describe, expect, it } from "vitest";
import { createSseParser, type SseMessage } from "./sse";

function collect(chunks: string[]): SseMessage[] {
  const messages: SseMessage[] = [];
  const parse = createSseParser((m) => messages.push(m));
  for (const chunk of chunks) {
    parse(chunk);
  }
  return messages;
}

describe("createSseParser", () => {
  it("parses named events", () => {
    expect(
      collect(['event: step\ndata: {"step":"aws"}\n\nevent: done\ndata: {"ok":true}\n\n']),
    ).toEqual([
      { event: "step", data: '{"step":"aws"}' },
      { event: "done", data: '{"ok":true}' },
    ]);
  });

  it("joins messages split across chunks", () => {
    expect(collect(["event: st", "ep\ndata: {", '"a":1}\n', "\n"])).toEqual([
      { event: "step", data: '{"a":1}' },
    ]);
  });

  it("accepts CRLF line endings and multi-line data", () => {
    expect(collect(["data: one\r\ndata: two\r\n\r\n"])).toEqual([
      { event: "message", data: "one\ntwo" },
    ]);
  });

  it("keeps an incomplete message until it ends", () => {
    expect(collect(["event: step\ndata: x\n"])).toEqual([]);
  });
});
