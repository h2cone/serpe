import type { SSEFrame } from "./types";

const KNOWN_FRAME_TYPES = new Set([
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

/** Narrow JSON into SSEFrame; unknown t or non-objects are dropped. */
function parseFrame(raw: string): SSEFrame | null {
  let v: unknown;
  try {
    v = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!v || typeof v !== "object" || Array.isArray(v)) return null;
  const t = (v as { t?: unknown }).t;
  if (typeof t !== "string" || !KNOWN_FRAME_TYPES.has(t)) return null;
  return v as SSEFrame;
}

/** Parse an SSE byte stream into frames (data: JSON lines). */
export async function* parseSSE(
  reader: ReadableStreamDefaultReader<Uint8Array>,
): AsyncGenerator<SSEFrame> {
  const decoder = new TextDecoder();
  let buffer = "";
  let dataLines: string[] = [];

  const flush = (): SSEFrame | null => {
    if (dataLines.length === 0) return null;
    const raw = dataLines.join("\n");
    dataLines = [];
    if (!raw) return null;
    return parseFrame(raw);
  };

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let nl: number;
    while ((nl = buffer.indexOf("\n")) >= 0) {
      let line = buffer.slice(0, nl);
      buffer = buffer.slice(nl + 1);
      if (line.endsWith("\r")) line = line.slice(0, -1);
      if (line === "") {
        const frame = flush();
        if (frame) yield frame;
        continue;
      }
      if (line.startsWith("data:")) {
        dataLines.push(line.slice(5).replace(/^ /, ""));
      }
      // ignore event:/id:/comment lines
    }
  }
  // trailing frame without blank line
  const frame = flush();
  if (frame) yield frame;
}

export async function* streamRun(
  body: { session_id: string; prompt: string },
  signal?: AbortSignal,
): AsyncGenerator<SSEFrame> {
  const res = await fetch("/api/runs", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "text/event-stream",
    },
    body: JSON.stringify(body),
    signal,
  });
  if (!res.ok || !res.body) {
    const text = await res.text().catch(() => "");
    throw new Error(`${res.status} ${res.statusText}${text ? `: ${text}` : ""}`);
  }
  yield* parseSSE(res.body.getReader());
}
