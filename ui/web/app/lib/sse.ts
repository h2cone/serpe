import { authorizationHeader, rejectAPIToken } from "./auth";
import { APIError } from "./api";
import {
  decodeSSEFrame,
  WireProtocolError,
  type SSEFrame,
} from "./wire";

const maxEventBytes = 2 << 20;
const maxStreamBytes = 32 << 20;

export class SSEDisconnectError extends Error {
  constructor() {
    super("The run stream disconnected before a terminal event. Reload the session before retrying.");
    this.name = "SSEDisconnectError";
  }
}

/** Parse JSON and apply the executable wire contract. */
function parseFrame(raw: string): SSEFrame | null {
  let v: unknown;
  try {
    v = JSON.parse(raw);
  } catch {
    throw new WireProtocolError("SSE data must be valid JSON");
  }
  return decodeSSEFrame(v);
}

/** Parse an SSE byte stream into frames (data: JSON lines). */
async function* parseSSEFrames(
  reader: ReadableStreamDefaultReader<Uint8Array>,
): AsyncGenerator<SSEFrame> {
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let buffer = "";
  let dataLines: string[] = [];
  let streamBytes = 0;
  let eventBytes = 0;
  let lineBytes = 0;

  const flush = (): SSEFrame | null => {
    if (dataLines.length === 0) return null;
    const raw = dataLines.join("\n");
    dataLines = [];
    eventBytes = 0;
    if (!raw) return null;
    return parseFrame(raw);
  };

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    streamBytes += value.byteLength;
    if (streamBytes > maxStreamBytes) {
      throw new WireProtocolError("SSE stream exceeds the browser safety limit");
    }
    for (const byte of value) {
      if (byte === 0x0a) {
        lineBytes = 0;
      } else if (++lineBytes > maxEventBytes) {
        throw new WireProtocolError("SSE line exceeds the browser safety limit");
      }
    }
    try {
      buffer += decoder.decode(value, { stream: true });
    } catch {
      throw new WireProtocolError("SSE stream must be valid UTF-8");
    }
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
        const data = line.slice(5).replace(/^ /, "");
        eventBytes += new TextEncoder().encode(data).byteLength + 1;
        if (eventBytes > maxEventBytes) {
          throw new WireProtocolError("SSE event exceeds the browser safety limit");
        }
        dataLines.push(data);
      }
      // ignore event:/id:/comment lines
    }
  }
  try {
    buffer += decoder.decode();
  } catch {
    throw new WireProtocolError("SSE stream must be valid UTF-8");
  }
  if (buffer) {
    let line = buffer;
    if (line.endsWith("\r")) line = line.slice(0, -1);
    if (line.startsWith("data:")) {
      const data = line.slice(5).replace(/^ /, "");
      eventBytes += new TextEncoder().encode(data).byteLength + 1;
      if (eventBytes > maxEventBytes) {
        throw new WireProtocolError("SSE event exceeds the browser safety limit");
      }
      dataLines.push(data);
    }
  }
  // trailing frame without blank line
  const frame = flush();
  if (frame) yield frame;
}

/** Parse an SSE byte stream and always release its reader. */
export async function* parseSSE(
  reader: ReadableStreamDefaultReader<Uint8Array>,
): AsyncGenerator<SSEFrame> {
  let completed = false;
  try {
    for await (const frame of parseSSEFrames(reader)) yield frame;
    completed = true;
  } finally {
    if (!completed) {
      try {
        await reader.cancel();
      } catch {
        // Preserve the parser/consumer error that caused cancellation.
      }
    }
    try {
      reader.releaseLock();
    } catch {
      // A hostile/custom stream implementation must not mask the root error.
    }
  }
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
      Authorization: authorizationHeader(),
    },
    body: JSON.stringify(body),
    signal,
    cache: "no-store",
    credentials: "omit",
    redirect: "error",
  });
  if (!res.ok || !res.body) {
    if (res.status === 401) rejectAPIToken();
    throw new APIError(
      res.status,
      res.status === 401
        ? "Authentication failed."
        : `The run request failed with HTTP ${res.status}.`,
    );
  }
  let terminal = false;
  for await (const frame of parseSSE(res.body.getReader())) {
    if (frame.t === "done" || frame.t === "error") terminal = true;
    yield frame;
  }
  if (!terminal) throw new SSEDisconnectError();
}
