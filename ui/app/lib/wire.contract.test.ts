import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { decodeSessionDetail, decodeSSEFrame, WireProtocolError } from "./wire";

describe("Go SSE wire contract", () => {
  it("decodes every concrete frame serialized by the server", () => {
    const fixture = JSON.parse(
      readFileSync(
        new URL("../../../server/testdata/sse_frames.json", import.meta.url),
        "utf8",
      ),
    ) as unknown[];
    const frames = fixture.map(decodeSSEFrame);
    expect(frames.every((frame) => frame !== null)).toBe(true);
    expect(frames.map((frame) => frame?.t)).toEqual([
      "run_start",
      "model_start",
      "part_start",
      "delta",
      "part_end",
      "tool_start",
      "tool_end",
      "model_end",
      "run_end",
      "error",
      "done",
    ]);
  });
});

describe("REST wire contract", () => {
  it("decodes the concrete session DTO serialized by the server", () => {
    const fixture = JSON.parse(
      readFileSync(
        new URL("../../../server/testdata/session_detail.json", import.meta.url),
        "utf8",
      ),
    ) as unknown;
    expect(decodeSessionDetail(fixture)).toMatchObject({
      id: "s1",
      message_count: 3,
    });
  });

  it("rejects malformed known content instead of casting it", () => {
    expect(() =>
      decodeSessionDetail({
        id: "s1",
        cwd: "/work",
        created_at: "now",
        updated_at: "now",
        message_count: 1,
        messages: [{ role: "user", content: [{ type: "text" }] }],
      }),
    ).toThrow(WireProtocolError);
  });
});
