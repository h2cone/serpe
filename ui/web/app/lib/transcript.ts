import type { ContentPart, Message, SSEFrame } from "./wire";

export type StreamToolStatus = "pending" | "running" | "done" | "failed";

/** One independently tracked in-flight tool call. */
export type StreamTool = {
  id?: string;
  name: string;
  argsText: string;
  turn: number;
  part?: number;
  index?: number;
  status: StreamToolStatus;
  result?: { content: ContentPart[]; is_error?: boolean };
};

export type StreamingState = {
  text: string;
  reasoning: string;
  refusal: string;
  /** A Map avoids prototype-key hazards from provider-controlled call IDs. */
  tools: Map<string, StreamTool>;
  usage?: Record<string, number>;
  finish?: string;
};

export type TranscriptState = {
  messages: Message[];
  /** Absolute index of messages[0] in the current detail snapshot. */
  messageStart: number;
  streaming: boolean;
  stream: StreamingState | null;
  error: string | null;
  stop: string | null;
  messageCount: number | null;
};

export function initialTranscript(
  messages: Message[] = [],
  messageStart = 0,
): TranscriptState {
  return {
    messages,
    messageStart,
    streaming: false,
    stream: null,
    error: null,
    stop: null,
    messageCount: messageStart + messages.length,
  };
}

function emptyStream(): StreamingState {
  return {
    text: "",
    reasoning: "",
    refusal: "",
    tools: new Map(),
  };
}

type ToolIdentity = {
  callID?: string;
  turn: number;
  part?: number;
  index?: number;
};

function findToolKey(
  tools: ReadonlyMap<string, StreamTool>,
  identity: ToolIdentity,
): string | undefined {
  for (const [key, tool] of tools) {
    if (identity.callID && tool.id === identity.callID) return key;
  }
  if (identity.index !== undefined) {
    for (const [key, tool] of tools) {
      if (tool.turn === identity.turn && tool.index === identity.index) return key;
    }
  }
  if (identity.part !== undefined) {
    for (const [key, tool] of tools) {
      if (tool.turn === identity.turn && tool.part === identity.part) return key;
    }
  }
  return undefined;
}

function preferredToolKey(identity: ToolIdentity): string {
  if (identity.callID) return `call:${identity.callID}`;
  if (identity.index !== undefined) {
    return `turn:${identity.turn}:index:${identity.index}`;
  }
  return `turn:${identity.turn}:part:${identity.part ?? 0}`;
}

function updateTool(
  stream: StreamingState,
  identity: ToolIdentity,
  update: (previous: StreamTool | undefined) => StreamTool,
): StreamingState {
  const tools = new Map(stream.tools);
  const oldKey = findToolKey(tools, identity);
  const nextKey = identity.callID ? preferredToolKey(identity) : oldKey ?? preferredToolKey(identity);
  const previous = oldKey ? tools.get(oldKey) : tools.get(nextKey);
  if (oldKey && oldKey !== nextKey) tools.delete(oldKey);
  tools.set(nextKey, update(previous));
  return { ...stream, tools };
}

function nextToolIndex(stream: StreamingState, turn: number): number {
  let next = 0;
  for (const tool of stream.tools.values()) {
    if (tool.turn === turn && tool.index !== undefined) {
      next = Math.max(next, tool.index + 1);
    }
  }
  return next;
}

function settleOpenTools(stream: StreamingState | null): StreamingState | null {
  if (!stream) return null;
  let changed = false;
  const tools = new Map<string, StreamTool>();
  for (const [key, tool] of stream.tools) {
    if (tool.status === "pending" || tool.status === "running") {
      tools.set(key, { ...tool, status: "failed" });
      changed = true;
    } else {
      tools.set(key, tool);
    }
  }
  return changed ? { ...stream, tools } : stream;
}

export function failStream(
  state: TranscriptState,
  message: string,
  stop?: string,
): TranscriptState {
  return {
    ...state,
    streaming: false,
    stream: settleOpenTools(state.stream),
    error: message,
    stop: stop ?? state.stop,
  };
}

export function applyFrame(
  state: TranscriptState,
  frame: SSEFrame,
): TranscriptState {
  switch (frame.t) {
    case "run_start":
      return {
        ...state,
        streaming: true,
        stream: emptyStream(),
        error: null,
        stop: null,
      };
    case "part_start": {
      if (!state.stream || frame.kind !== "tool_call") return state;
      const index = nextToolIndex(state.stream, frame.turn);
      const stream = updateTool(
        state.stream,
        {
          callID: frame.call_id,
          turn: frame.turn,
          part: frame.part,
          index,
        },
        (previous) => ({
          id: frame.call_id ?? previous?.id,
          name: frame.name ?? previous?.name ?? "tool",
          argsText: previous?.argsText ?? "",
          turn: frame.turn,
          part: frame.part,
          index: previous?.index ?? index,
          status: previous?.status ?? "pending",
          result: previous?.result,
        }),
      );
      return { ...state, stream };
    }
    case "delta": {
      if (!state.stream) return state;
      if (frame.kind === "text") {
        return {
          ...state,
          stream: { ...state.stream, text: state.stream.text + frame.text },
        };
      }
      if (frame.kind === "reasoning_summary") {
        return {
          ...state,
          stream: {
            ...state.stream,
            reasoning: state.stream.reasoning + frame.text,
          },
        };
      }
      if (frame.kind === "refusal") {
        return {
          ...state,
          stream: {
            ...state.stream,
            refusal: state.stream.refusal + frame.text,
          },
        };
      }
      if (frame.kind !== "tool_arguments") return state;
      const stream = updateTool(
        state.stream,
        {
          callID: frame.call_id,
          turn: frame.turn,
          part: frame.part,
        },
        (previous) => ({
          id: frame.call_id ?? previous?.id,
          name: previous?.name ?? "tool",
          argsText: (previous?.argsText ?? "") + frame.text,
          turn: frame.turn,
          part: frame.part,
          index: previous?.index,
          status: previous?.status ?? "pending",
          result: previous?.result,
        }),
      );
      return { ...state, stream };
    }
    case "part_end":
      return state;
    case "tool_start": {
      if (!state.stream) return state;
      const stream = updateTool(
        state.stream,
        { callID: frame.call.id, turn: frame.turn, index: frame.idx },
        (previous) => ({
          id: frame.call.id,
          name: frame.call.name,
          argsText:
            previous?.argsText ||
            (frame.call.arguments ? JSON.stringify(frame.call.arguments) : ""),
          turn: frame.turn,
          part: previous?.part,
          index: frame.idx,
          status: "running",
          result: previous?.result,
        }),
      );
      return { ...state, stream };
    }
    case "tool_end": {
      if (!state.stream) return state;
      const stream = updateTool(
        state.stream,
        { callID: frame.call.id, turn: frame.turn, index: frame.idx },
        (previous) => ({
          id: frame.call.id,
          name: frame.call.name,
          argsText:
            previous?.argsText ||
            (frame.call.arguments ? JSON.stringify(frame.call.arguments) : ""),
          turn: frame.turn,
          part: previous?.part,
          index: frame.idx,
          status: frame.result.is_error ? "failed" : "done",
          result: frame.result,
        }),
      );
      return { ...state, stream };
    }
    case "model_end":
      if (!state.stream) return state;
      return {
        ...state,
        stream: {
          ...state.stream,
          usage: frame.usage,
          finish: frame.finish,
        },
      };
    case "run_end":
      return { ...state, stop: frame.stop };
    case "done":
      return {
        ...state,
        streaming: false,
        stream: settleOpenTools(state.stream),
        stop: frame.stop,
        messageCount: frame.message_count,
        error: null,
      };
    case "error":
      return failStream(state, frame.message, frame.stop);
    default:
      return state;
  }
}

/** Append a local user message before streaming starts. */
export function appendUser(
  state: TranscriptState,
  prompt: string,
): TranscriptState {
  return {
    ...state,
    messages: [
      ...state.messages,
      { role: "user", content: [{ type: "text", text: prompt }] },
    ],
  };
}

/** Prepend one cursor page while preserving absolute message indices. */
export function prependMessages(
  state: TranscriptState,
  messages: Message[],
  messageStart: number,
): TranscriptState {
  if (messageStart < 0 || messageStart + messages.length !== state.messageStart) {
    throw new Error("The older message page does not join the current snapshot");
  }
  return {
    ...state,
    messages: [...messages, ...state.messages],
    messageStart,
  };
}
