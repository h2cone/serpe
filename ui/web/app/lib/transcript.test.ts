import { describe, expect, it } from "vitest";
import { applyFrame, appendUser, initialTranscript } from "./transcript";
import type { SSEFrame } from "./wire";

function applyAll(frames: SSEFrame[]) {
  let s = initialTranscript();
  s = appendUser(s, "hi");
  for (const f of frames) s = applyFrame(s, f);
  return s;
}

describe("transcript reducer", () => {
  it("builds text stream then done without folding into messages", () => {
    const s = applyAll([
      { t: "run_start" },
      { t: "model_start", turn: 1 },
      { t: "part_start", turn: 1, part: 0, kind: "text" },
      { t: "delta", turn: 1, part: 0, kind: "text", text: "Hel" },
      { t: "delta", turn: 1, part: 0, kind: "text", text: "lo" },
      { t: "part_end", turn: 1, part: 0 },
      { t: "model_end", turn: 1, usage: { total_tokens: 3 }, finish: "stop" },
      { t: "run_end", stop: "completed" },
      { t: "done", session_id: "s1", stop: "completed", message_count: 2 },
    ]);
    expect(s.streaming).toBe(false);
    expect(s.error).toBeNull();
    expect(s.messageCount).toBe(2);
    expect(s.stop).toBe("completed");
    // Stream kept for display until revalidate; messages stay pre-run + user only.
    expect(s.stream?.text).toBe("Hello");
    expect(s.messages.every((m) => m.role === "user")).toBe(true);
  });

  it("unifies tool arg stream and execution on one tools map", () => {
    const s = applyAll([
      { t: "run_start" },
      {
        t: "part_start",
        turn: 1,
        part: 0,
        kind: "tool_call",
        call_id: "c1",
        name: "now",
      },
      {
        t: "delta",
        turn: 1,
        part: 0,
        kind: "tool_arguments",
        text: '{"x":',
        call_id: "c1",
      },
      {
        t: "delta",
        turn: 1,
        part: 0,
        kind: "tool_arguments",
        text: "1}",
        call_id: "c1",
      },
      {
        t: "tool_start",
        turn: 1,
        idx: 0,
        call: { id: "c1", name: "now", arguments: { x: 1 } },
      },
      {
        t: "tool_end",
        turn: 1,
        idx: 0,
        call: { id: "c1", name: "now" },
        result: {
          content: [{ type: "text", text: "ok" }],
          is_error: false,
        },
      },
      { t: "run_end", stop: "completed" },
      { t: "done", session_id: "s1", stop: "completed", message_count: 4 },
    ]);
    expect(s.streaming).toBe(false);
    expect(s.error).toBeNull();
    const tool = Array.from(s.stream?.tools.values() ?? []).find(
      (candidate) => candidate.id === "c1",
    );
    expect(tool).toMatchObject({
      id: "c1",
      name: "now",
      argsText: '{"x":1}',
      status: "done",
    });
    expect(tool?.result?.content).toEqual([{ type: "text", text: "ok" }]);
  });

  it("error ends stream without treating partial as committed", () => {
    const s = applyAll([
      { t: "run_start" },
      { t: "delta", turn: 1, part: 0, kind: "text", text: "partial" },
      { t: "error", message: "cancelled", stop: "cancelled" },
    ]);
    expect(s.streaming).toBe(false);
    expect(s.error).toBe("cancelled");
    expect(s.stop).toBe("cancelled");
    expect(s.messages.every((m) => m.role === "user")).toBe(true);
  });

  it("distinguishes run_end from done", () => {
    let s = initialTranscript();
    s = applyFrame(s, { t: "run_start" });
    s = applyFrame(s, { t: "run_end", stop: "max_model_turns" });
    expect(s.streaming).toBe(true);
    expect(s.stop).toBe("max_model_turns");
  });

  it("keeps out-of-order tool completions independent and settles failures", () => {
    const s = applyAll([
      { t: "run_start" },
      {
        t: "part_start",
        turn: 1,
        part: 0,
        kind: "tool_call",
        call_id: "__proto__",
        name: "first",
      },
      {
        t: "part_start",
        turn: 1,
        part: 1,
        kind: "tool_call",
        call_id: "c2",
        name: "second",
      },
      {
        t: "tool_start",
        turn: 1,
        idx: 0,
        call: { id: "__proto__", name: "first" },
      },
      {
        t: "tool_start",
        turn: 1,
        idx: 1,
        call: { id: "c2", name: "second" },
      },
      {
        t: "tool_end",
        turn: 1,
        idx: 1,
        call: { id: "c2", name: "second" },
        result: { content: [{ type: "text", text: "second done" }] },
      },
      { t: "error", message: "connection lost" },
    ]);
    const tools = Array.from(s.stream?.tools.values() ?? []);
    expect(tools).toHaveLength(2);
    expect(tools.find((tool) => tool.id === "c2")?.status).toBe("done");
    expect(tools.find((tool) => tool.id === "__proto__")?.status).toBe(
      "failed",
    );
    expect(s.streaming).toBe(false);
  });

  it("rekeys a provisional part when its call ID arrives", () => {
    const s = applyAll([
      { t: "run_start" },
      { t: "part_start", turn: 1, part: 3, kind: "tool_call", name: "read" },
      {
        t: "delta",
        turn: 1,
        part: 3,
        kind: "tool_arguments",
        call_id: "call-3",
        text: "{}",
      },
    ]);
    const tools = Array.from(s.stream?.tools.values() ?? []);
    expect(tools).toHaveLength(1);
    expect(tools[0]).toMatchObject({ id: "call-3", argsText: "{}" });
  });
});
