import { afterEach, describe, expect, it, vi } from "vitest";
import { parseSSE, SSEDisconnectError, streamRun } from "./sse";
import { WireProtocolError } from "./wire";

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

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

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
    const ok =
      'data: {"t":"error",\n' + 'data: "message":"x"}\n\n';
    // Actually SSE multi-data join with newline: {"t":"error",\n"message":"x"}
    const frames = [];
    for await (const f of parseSSE(readerFrom([ok]))) frames.push(f);
    expect(frames[0]).toEqual({ t: "error", message: "x" });
  });

  it("rejects a known frame with missing required fields", async () => {
    const consume = async () => {
      for await (const _ of parseSSE(
        readerFrom(['data: {"t":"tool_start","turn":1,"idx":0}\n\n']),
      )) {
        // consume the generator so boundary validation runs
      }
    };
    await expect(consume()).rejects.toBeInstanceOf(WireProtocolError);
  });

  it("ignores unknown frame types for forward compatibility", async () => {
    const frames = [];
    for await (const frame of parseSSE(
      readerFrom(['data: {"t":"future_frame","value":1}\n\n']),
    )) {
      frames.push(frame);
    }
    expect(frames).toEqual([]);
  });

  it("rejects malformed JSON instead of silently skipping it", async () => {
    const consume = async () => {
      for await (const _ of parseSSE(readerFrom(["data: {broken}\n\n"]))) {
        // consume
      }
    };
    await expect(consume()).rejects.toBeInstanceOf(WireProtocolError);
  });

  it("cancels and releases the reader after a protocol failure", async () => {
    const cancel = vi.fn(async () => {});
    const releaseLock = vi.fn();
    const reader = readerFrom(["data: {broken}\n\n"]);
    reader.cancel = cancel;
    reader.releaseLock = releaseLock;
    const consume = async () => {
      for await (const _ of parseSSE(reader)) {
        // consume
      }
    };
    await expect(consume()).rejects.toBeInstanceOf(WireProtocolError);
    expect(cancel).toHaveBeenCalledOnce();
    expect(releaseLock).toHaveBeenCalledOnce();
  });

  it("rejects malformed UTF-8 and releases the reader", async () => {
    const releaseLock = vi.fn();
    let read = false;
    const reader = {
      read: async () => {
        if (read) return { done: true, value: undefined };
        read = true;
        return { done: false, value: new Uint8Array([0xff]) };
      },
      cancel: async () => {},
      closed: Promise.resolve(undefined),
      releaseLock,
    } as ReadableStreamDefaultReader<Uint8Array>;
    const consume = async () => {
      for await (const _ of parseSSE(reader)) {
        // consume
      }
    };
    await expect(consume()).rejects.toBeInstanceOf(WireProtocolError);
    expect(releaseLock).toHaveBeenCalledOnce();
  });

  it("requires a terminal event and uses a bare fetch", async () => {
    vi.stubGlobal("window", {});
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new TextEncoder().encode('data: {"t":"run_start"}\n\n'));
        controller.close();
      },
    });
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        new Response(body, { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const consume = async () => {
      for await (const _ of streamRun({ session_id: "s1", prompt: "hi" })) {
        // consume
      }
    };
    await expect(consume()).rejects.toBeInstanceOf(SSEDisconnectError);
    const init = fetchMock.mock.calls[0][1];
    expect(init).toBeDefined();
    expect(init?.credentials).toBe("omit");
  });
});
