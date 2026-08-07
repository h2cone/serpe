import { describe, expect, it } from "vitest";
import { parseSSE } from "./sse";

function readerFrom(chunks: string[]): ReadableStreamDefaultReader<Uint8Array> {
  const enc = new TextEncoder();
  let i = 0;
  return {
    read: async () => {
      if (i >= chunks.length) return { done: true, value: undefined };
      const value = enc.encode(chunks[i++]);
      return { done: false, value };
    },
    cancel: async () => {},
    closed: Promise.resolve(undefined),
    releaseLock: () => {},
  } as ReadableStreamDefaultReader<Uint8Array>;
}

describe("parseSSE", () => {
  it("parses multiple frames", async () => {
    const body =
      'data: {"t":"run_start"}\n\n' +
      'data: {"t":"delta","turn":1,"part":0,"kind":"text","text":"a"}\n\n' +
      'data: {"t":"done","session_id":"s","stop":"completed","message_count":2}\n\n';
    const frames = [];
    for await (const f of parseSSE(readerFrom([body]))) frames.push(f);
    expect(frames.map((f) => f.t)).toEqual(["run_start", "delta", "done"]);
  });

  it("handles chunked lines", async () => {
    const frames = [];
    for await (const f of parseSSE(
      readerFrom(['data: {"t":"run_', 'start"}\n', "\n"]),
    )) {
      frames.push(f);
    }
    expect(frames).toEqual([{ t: "run_start" }]);
  });

  it("joins multi-line data", async () => {
    const body = "data: {\"t\":\"error\",\n\ndata: \"message\":\"x\"}\n\n";
    // simpler: two data lines that form invalid JSON alone — use valid join
    const ok =
      'data: {"t":"error",\n' + 'data: "message":"x"}\n\n';
    // Actually SSE multi-data join with newline: {"t":"error",\n"message":"x"}
    const frames = [];
    for await (const f of parseSSE(readerFrom([ok]))) frames.push(f);
    expect(frames[0]).toEqual({ t: "error", message: "x" });
  });
});
