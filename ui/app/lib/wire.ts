/** Runtime-checked HTTP/SSE wire contract shared by UI consumers. */

export type ContentPart =
  | { type: "text"; text: string }
  | { type: "reasoning_summary"; text: string }
  | { type: "refusal"; text: string }
  | {
      type: "tool_call";
      id: string;
      name: string;
      arguments?: Record<string, unknown>;
    }
  | {
      type: "tool_result";
      call_id: string;
      name: string;
      is_error?: boolean;
      content: ContentPart[];
    }
  | {
      type: "image";
      mime?: string;
      uri?: string;
      /** base64 payload when uri is absent */
      data?: string;
      detail?: string;
    };

export type Message = {
  role: "user" | "assistant";
  content: ContentPart[];
};

export type ToolCallWire = {
  type?: string;
  id: string;
  name: string;
  arguments?: Record<string, unknown>;
};

type JSONObject = Record<string, unknown>;

/** A server payload violated a known HTTP/SSE wire shape. */
export class WireProtocolError extends Error {
  constructor(message: string) {
    super(`invalid wire payload: ${message}`);
    this.name = "WireProtocolError";
  }
}

function fail(path: string, expected: string): never {
  throw new WireProtocolError(`${path} must be ${expected}`);
}

function object(value: unknown, path: string): JSONObject {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return fail(path, "an object");
  }
  return value as JSONObject;
}

function stringField(value: JSONObject, key: string, path = key): string {
  const field = value[key];
  if (typeof field !== "string") return fail(path, "a string");
  return field;
}

function optionalString(value: JSONObject, key: string, path = key): string | undefined {
  const field = value[key];
  if (field === undefined) return undefined;
  if (typeof field !== "string") return fail(path, "a string when present");
  return field;
}

function optionalBoolean(value: JSONObject, key: string, path = key): boolean | undefined {
  const field = value[key];
  if (field === undefined) return undefined;
  if (typeof field !== "boolean") return fail(path, "a boolean when present");
  return field;
}

function integerField(value: JSONObject, key: string, path = key): number {
  const field = value[key];
  if (typeof field !== "number" || !Number.isSafeInteger(field) || field < 0) {
    return fail(path, "a non-negative safe integer");
  }
  return field;
}

function optionalRecord(value: JSONObject, key: string, path = key): JSONObject | undefined {
  const field = value[key];
  if (field === undefined) return undefined;
  return object(field, path);
}

function optionalUsage(value: JSONObject): Record<string, number> | undefined {
  const usage = optionalRecord(value, "usage");
  if (usage === undefined) return undefined;
  const checked: Record<string, number> = {};
  for (const [key, count] of Object.entries(usage)) {
    if (typeof count !== "number" || !Number.isFinite(count) || count < 0) {
      return fail(`usage.${key}`, "a non-negative number");
    }
    checked[key] = count;
  }
  return checked;
}

function contentPart(value: unknown, path: string): ContentPart {
  const part = object(value, path);
  const type = stringField(part, "type", `${path}.type`);
  switch (type) {
    case "text":
    case "reasoning_summary":
    case "refusal":
      return { type, text: stringField(part, "text", `${path}.text`) };
    case "tool_call": {
      const args = optionalRecord(part, "arguments", `${path}.arguments`);
      return {
        type,
        id: stringField(part, "id", `${path}.id`),
        name: stringField(part, "name", `${path}.name`),
        ...(args === undefined ? {} : { arguments: args }),
      };
    }
    case "tool_result": {
      const content = contentParts(part.content, `${path}.content`);
      const isError = optionalBoolean(part, "is_error", `${path}.is_error`);
      return {
        type,
        call_id: stringField(part, "call_id", `${path}.call_id`),
        name: stringField(part, "name", `${path}.name`),
        content,
        ...(isError === undefined ? {} : { is_error: isError }),
      };
    }
    case "image": {
      const mime = optionalString(part, "mime", `${path}.mime`);
      const uri = optionalString(part, "uri", `${path}.uri`);
      const data = optionalString(part, "data", `${path}.data`);
      const detail = optionalString(part, "detail", `${path}.detail`);
      if (uri === undefined && (mime === undefined || data === undefined)) {
        return fail(path, "an image URI or inline mime/data pair");
      }
      if (uri !== undefined && (mime !== undefined || data !== undefined)) {
        return fail(path, "exactly one image source");
      }
      return {
        type,
        ...(mime === undefined ? {} : { mime }),
        ...(uri === undefined ? {} : { uri }),
        ...(data === undefined ? {} : { data }),
        ...(detail === undefined ? {} : { detail }),
      };
    }
    default:
      return fail(`${path}.type`, "a known content type");
  }
}

function contentParts(value: unknown, path: string): ContentPart[] {
  if (!Array.isArray(value)) return fail(path, "an array");
  return value.map((part, index) => contentPart(part, `${path}[${index}]`));
}

function message(value: unknown, path: string): Message {
  const input = object(value, path);
  const role = stringField(input, "role", `${path}.role`);
  if (role !== "user" && role !== "assistant") {
    return fail(`${path}.role`, '"user" or "assistant"');
  }
  return {
    role,
    content: contentParts(input.content, `${path}.content`),
  };
}

function sessionSummary(value: unknown, path: string) {
  const input = object(value, path);
  const title = optionalString(input, "title", `${path}.title`);
  const parentID = optionalString(input, "parent_id", `${path}.parent_id`);
  const preview = optionalString(input, "preview", `${path}.preview`);
  return {
    id: stringField(input, "id", `${path}.id`),
    ...(title === undefined ? {} : { title }),
    cwd: stringField(input, "cwd", `${path}.cwd`),
    ...(parentID === undefined ? {} : { parent_id: parentID }),
    created_at: stringField(input, "created_at", `${path}.created_at`),
    updated_at: stringField(input, "updated_at", `${path}.updated_at`),
    message_count: integerField(input, "message_count", `${path}.message_count`),
    ...(preview === undefined ? {} : { preview }),
  };
}

export type SessionSummary = ReturnType<typeof sessionSummary>;
export type SessionDetail = SessionSummary & { messages: Message[] };

export function decodeSessionSummary(value: unknown): SessionSummary {
  return sessionSummary(value, "session");
}

export function decodeSessionSummaries(value: unknown): SessionSummary[] {
  if (!Array.isArray(value)) return fail("sessions", "an array");
  return value.map((item, index) => sessionSummary(item, `sessions[${index}]`));
}

export function decodeSessionDetail(value: unknown): SessionDetail {
  const input = object(value, "session");
  const summary = sessionSummary(input, "session");
  if (!Array.isArray(input.messages)) return fail("session.messages", "an array");
  return {
    ...summary,
    messages: input.messages.map((item, index) =>
      message(item, `session.messages[${index}]`),
    ),
  };
}

function toolCall(value: unknown, path: string): ToolCallWire {
  const call = object(value, path);
  const type = optionalString(call, "type", `${path}.type`);
  if (type !== undefined && type !== "tool_call") {
    return fail(`${path}.type`, '"tool_call" when present');
  }
  const args = optionalRecord(call, "arguments", `${path}.arguments`);
  return {
    ...(type === undefined ? {} : { type }),
    id: stringField(call, "id", `${path}.id`),
    name: stringField(call, "name", `${path}.name`),
    ...(args === undefined ? {} : { arguments: args }),
  };
}

// Payload decoders are the single TypeScript declaration of frame fields.
// SSEFrame is inferred from this table, and dispatch uses the same keys.
const payloadDecoders = {
  run_start: (_: JSONObject) => ({}),
  model_start: (frame: JSONObject) => ({ turn: integerField(frame, "turn") }),
  part_start: (frame: JSONObject) => {
    const callID = optionalString(frame, "call_id");
    const name = optionalString(frame, "name");
    return {
      turn: integerField(frame, "turn"),
      part: integerField(frame, "part"),
      kind: stringField(frame, "kind"),
      ...(callID === undefined ? {} : { call_id: callID }),
      ...(name === undefined ? {} : { name }),
    };
  },
  delta: (frame: JSONObject) => {
    const callID = optionalString(frame, "call_id");
    return {
      turn: integerField(frame, "turn"),
      part: integerField(frame, "part"),
      kind: stringField(frame, "kind"),
      text: stringField(frame, "text"),
      ...(callID === undefined ? {} : { call_id: callID }),
    };
  },
  part_end: (frame: JSONObject) => {
    const callID = optionalString(frame, "call_id");
    return {
      turn: integerField(frame, "turn"),
      part: integerField(frame, "part"),
      ...(callID === undefined ? {} : { call_id: callID }),
    };
  },
  tool_start: (frame: JSONObject) => ({
    turn: integerField(frame, "turn"),
    idx: integerField(frame, "idx"),
    call: toolCall(frame.call, "call"),
  }),
  tool_end: (frame: JSONObject) => {
    const result = object(frame.result, "result");
    const isError = optionalBoolean(result, "is_error", "result.is_error");
    return {
      turn: integerField(frame, "turn"),
      idx: integerField(frame, "idx"),
      call: toolCall(frame.call, "call"),
      result: {
        content: contentParts(result.content, "result.content"),
        ...(isError === undefined ? {} : { is_error: isError }),
      },
    };
  },
  model_end: (frame: JSONObject) => {
    const usage = optionalUsage(frame);
    const finish = optionalString(frame, "finish");
    return {
      turn: integerField(frame, "turn"),
      ...(usage === undefined ? {} : { usage }),
      ...(finish === undefined ? {} : { finish }),
    };
  },
  run_end: (frame: JSONObject) => ({ stop: stringField(frame, "stop") }),
  error: (frame: JSONObject) => {
    const stop = optionalString(frame, "stop");
    return {
      message: stringField(frame, "message"),
      ...(stop === undefined ? {} : { stop }),
    };
  },
  done: (frame: JSONObject) => ({
    session_id: stringField(frame, "session_id"),
    stop: stringField(frame, "stop"),
    message_count: integerField(frame, "message_count"),
  }),
};

type FrameType = keyof typeof payloadDecoders;
type FrameFor<K extends FrameType> = {
  t: K;
} & ReturnType<(typeof payloadDecoders)[K]>;

export type SSEFrame = {
  [K in FrameType]: FrameFor<K>;
}[FrameType];

function hasDecoder(type: string): type is FrameType {
  return Object.prototype.hasOwnProperty.call(payloadDecoders, type);
}

/**
 * Decode an already-parsed JSON value. Unknown frame types are ignored for
 * forward compatibility; known types with invalid fields raise a diagnostic
 * protocol error instead of crossing the UI boundary as a false typed value.
 */
export function decodeSSEFrame(value: unknown): SSEFrame | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const frame = value as JSONObject;
  if (typeof frame.t !== "string" || !hasDecoder(frame.t)) return null;
  const type = frame.t;
  const decode = payloadDecoders[type] as (input: JSONObject) => JSONObject;
  return { t: type, ...decode(frame) } as SSEFrame;
}
