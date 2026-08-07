import type { ContentPart, Message, SSEFrame } from "./types";

/** One in-flight tool: arg streaming + optional execution status/result. */
export type StreamTool = {
  id: string;
  name: string;
  argsText: string;
  part?: number;
  status?: "running" | "done";
  result?: { content: ContentPart[]; is_error?: boolean };
};

export type StreamingState = {
  text: string;
  reasoning: string;
  refusal: string;
  /** Tools keyed by call_id (or part index before id is known). */
  tools: Record<string, StreamTool>;
  usage?: Record<string, number>;
  finish?: string;
};

export type TranscriptState = {
  messages: Message[];
  streaming: boolean;
  stream: StreamingState | null;
  error: string | null;
  stop: string | null;
  messageCount: number | null;
};

export function initialTranscript(messages: Message[] = []): TranscriptState {
  return {
    messages,
    streaming: false,
    stream: null,
    error: null,
    stop: null,
    messageCount: messages.length,
  };
}

function emptyStream(): StreamingState {
  return {
    text: "",
    reasoning: "",
    refusal: "",
    tools: {},
  };
}

function toolKey(
  callId: string | undefined,
  part: number | undefined,
  tools: Record<string, StreamTool>,
): string {
  if (callId) return callId;
  if (part !== undefined) {
    const byPart = Object.keys(tools).find((k) => tools[k].part === part);
    if (byPart) return byPart;
    return `p${part}`;
  }
  return `p0`;
}

export function applyFrame(state: TranscriptState, frame: SSEFrame): TranscriptState {
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
      if (!state.stream) return state;
      const stream = { ...state.stream, tools: { ...state.stream.tools } };
      if (frame.kind === "tool_call") {
        const key = toolKey(frame.call_id, frame.part, stream.tools);
        stream.tools[key] = {
          id: frame.call_id ?? key,
          name: frame.name ?? "tool",
          argsText: stream.tools[key]?.argsText ?? "",
          part: frame.part,
          status: stream.tools[key]?.status,
          result: stream.tools[key]?.result,
        };
      }
      return { ...state, stream };
    }
    case "delta": {
      if (!state.stream) return state;
      const stream = { ...state.stream, tools: { ...state.stream.tools } };
      if (frame.kind === "text") {
        stream.text += frame.text ?? "";
      } else if (frame.kind === "reasoning_summary") {
        stream.reasoning += frame.text ?? "";
      } else if (frame.kind === "refusal") {
        stream.refusal += frame.text ?? "";
      } else if (frame.kind === "tool_arguments") {
        const key = toolKey(frame.call_id, frame.part, stream.tools);
        const prev = stream.tools[key] ?? {
          id: frame.call_id ?? key,
          name: "tool",
          argsText: "",
          part: frame.part,
        };
        stream.tools[key] = {
          ...prev,
          argsText: prev.argsText + (frame.text ?? ""),
        };
      }
      return { ...state, stream };
    }
    case "part_end":
      return state;
    case "tool_start": {
      if (!state.stream) return state;
      const key = toolKey(frame.call.id, undefined, state.stream.tools);
      const prev = state.stream.tools[key];
      const argsText =
        prev?.argsText ||
        (frame.call.arguments
          ? JSON.stringify(frame.call.arguments)
          : "");
      return {
        ...state,
        stream: {
          ...state.stream,
          tools: {
            ...state.stream.tools,
            [key]: {
              id: frame.call.id,
              name: frame.call.name,
              argsText,
              part: prev?.part,
              status: "running",
              result: prev?.result,
            },
          },
        },
      };
    }
    case "tool_end": {
      if (!state.stream) return state;
      const key = toolKey(frame.call.id, undefined, state.stream.tools);
      const prev = state.stream.tools[key];
      const argsText =
        prev?.argsText ||
        (frame.call.arguments
          ? JSON.stringify(frame.call.arguments)
          : "");
      return {
        ...state,
        stream: {
          ...state.stream,
          tools: {
            ...state.stream.tools,
            [key]: {
              id: frame.call.id,
              name: frame.call.name,
              argsText,
              part: prev?.part,
              status: "done",
              result: frame.result,
            },
          },
        },
      };
    }
    case "model_end": {
      if (!state.stream) return state;
      return {
        ...state,
        stream: {
          ...state.stream,
          usage: frame.usage,
          finish: frame.finish,
        },
      };
    }
    case "run_end":
      return { ...state, stop: frame.stop };
    case "done":
      // Do not fold stream into messages: multi-tool turns need assistant
      // tool_call + user tool_result rows that only the server transcript has.
      // Keep stream UI until revalidate replaces messages (ChatView useEffect).
      return {
        ...state,
        streaming: false,
        stop: frame.stop,
        messageCount: frame.message_count,
        error: null,
      };
    case "error":
      return {
        ...state,
        streaming: false,
        error: frame.message,
        stop: frame.stop ?? state.stop,
      };
    default:
      return state;
  }
}

/** Append a local user message before streaming starts. */
export function appendUser(state: TranscriptState, prompt: string): TranscriptState {
  return {
    ...state,
    messages: [
      ...state.messages,
      { role: "user", content: [{ type: "text", text: prompt }] },
    ],
  };
}
