/** Wire DTO types mirrored from server (ContentRecord + SSEFrame). */

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
      /** base64 payload when uri is absent (ContentRecord stable shape) */
      data?: string;
      detail?: string;
    };

export type Message = {
  role: "user" | "assistant";
  content: ContentPart[];
};

export type SessionSummary = {
  id: string;
  title?: string;
  cwd: string;
  parent_id?: string;
  created_at: string;
  updated_at: string;
  message_count: number;
  preview?: string;
};

export type SessionDetail = SessionSummary & {
  messages: Message[];
};

export type ToolCallWire = {
  type?: string;
  id: string;
  name: string;
  arguments?: Record<string, unknown>;
};

export type SSEFrame =
  | { t: "run_start" }
  | { t: "model_start"; turn: number }
  | {
      t: "part_start";
      turn: number;
      part: number;
      kind: string;
      call_id?: string;
      name?: string;
    }
  | {
      t: "delta";
      turn: number;
      part: number;
      kind: string;
      text: string;
      call_id?: string;
    }
  | { t: "part_end"; turn: number; part: number; call_id?: string }
  | {
      t: "tool_start";
      turn: number;
      idx: number;
      call: ToolCallWire;
    }
  | {
      t: "tool_end";
      turn: number;
      idx: number;
      call: ToolCallWire;
      result: { content: ContentPart[]; is_error?: boolean };
    }
  | { t: "model_end"; turn: number; usage?: Record<string, number>; finish?: string }
  | { t: "run_end"; stop: string }
  | { t: "error"; message: string; stop?: string }
  | { t: "done"; session_id: string; stop: string; message_count: number };
