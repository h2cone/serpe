import { useCallback, useEffect, useRef, useState } from "react";
import { useParams, useRevalidator } from "react-router";
import { streamRun } from "~/lib/sse";
import {
  appendUser,
  applyFrame,
  initialTranscript,
  type StreamingState,
  type TranscriptState,
} from "~/lib/transcript";
import type { Message, SessionSummary } from "~/lib/wire";
import { Composer } from "./composer";
import { ContentPartView, ToolCallCard } from "./content-part";

export function ChatView({
  meta,
  initialMessages,
}: {
  meta: SessionSummary;
  initialMessages: Message[];
}) {
  const revalidator = useRevalidator();
  const params = useParams();
  const [state, setState] = useState<TranscriptState>(() =>
    initialTranscript(initialMessages),
  );
  const abortRef = useRef<AbortController | null>(null);
  const bottomRef = useRef<HTMLDivElement>(null);

  // Reset when session changes or loader revalidates messages.
  useEffect(() => {
    setState(initialTranscript(initialMessages));
  }, [meta.id, initialMessages]);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [state.messages, state.stream]);

  const send = useCallback(
    async (prompt: string) => {
      if (!prompt.trim() || state.streaming) return;
      const ac = new AbortController();
      abortRef.current = ac;
      setState((s) => appendUser(s, prompt));
      try {
        for await (const frame of streamRun(
          { session_id: meta.id, prompt },
          ac.signal,
        )) {
          setState((s) => applyFrame(s, frame));
        }
        revalidator.revalidate();
      } catch (e) {
        if ((e as Error).name === "AbortError") {
          setState((s) => ({
            ...s,
            streaming: false,
            error: s.error ?? "cancelled",
            stop: "cancelled",
          }));
        } else {
          setState((s) => ({
            ...s,
            streaming: false,
            error: e instanceof Error ? e.message : String(e),
          }));
        }
      } finally {
        abortRef.current = null;
      }
    },
    [meta.id, state.streaming, revalidator],
  );

  const stop = () => abortRef.current?.abort();

  return (
    <>
      <div className="chat-scroll">
        <MessageList state={state} />
        <div ref={bottomRef} />
      </div>
      {state.error && (
        <div className="error-banner">
          {state.error}
          {state.stop ? ` (${state.stop})` : ""}
        </div>
      )}
      <Composer
        streaming={state.streaming}
        onSend={send}
        onStop={stop}
        key={params.id}
      />
    </>
  );
}

function MessageList({ state }: { state: TranscriptState }) {
  return (
    <div className="message-list">
      {state.messages.map((m, i) => (
        <MessageBubble key={i} message={m} />
      ))}
      {state.stream && <StreamingAssistant stream={state.stream} />}
    </div>
  );
}

function MessageBubble({ message }: { message: Message }) {
  const containsToolActivity = message.content.some(
    (part) => part.type === "tool_call" || part.type === "tool_result",
  );
  const isToolActivity =
    message.content.length > 0 &&
    containsToolActivity &&
    message.content.every(
      (part) =>
        part.type === "tool_call" ||
        part.type === "tool_result" ||
        part.type === "reasoning_summary",
    );
  const isUser = message.role === "user" && !isToolActivity;
  const kind = isToolActivity ? "activity" : isUser ? "user" : "assistant";
  return (
    <article className={`message-row ${kind}`} aria-label={isUser ? "You" : "serpe"}>
      <div className={`message-bubble ${kind}`}>
        {message.content.map((p, i) => (
          <ContentPartView key={i} part={p} />
        ))}
      </div>
    </article>
  );
}

function StreamingAssistant({ stream }: { stream: StreamingState }) {
  const tools = Object.values(stream.tools);
  return (
    <article className="message-row assistant" aria-label="serpe">
      <div className="message-bubble streaming">
        {stream.reasoning && (
          <details open className="thinking">
            <summary>Thinking</summary>
            <p>{stream.reasoning}</p>
          </details>
        )}
        {stream.refusal && (
          <p className="refusal">{stream.refusal}</p>
        )}
        {tools.map((t) => {
          let args: Record<string, unknown> | undefined;
          try {
            if (t.argsText) {
              args = JSON.parse(t.argsText) as Record<string, unknown>;
            }
          } catch {
            args = undefined;
          }
          return (
            <ToolCallCard
              key={t.id}
              name={t.name}
              args={args}
              argsText={!args ? t.argsText : undefined}
              status={t.status ?? "running"}
              result={t.result}
            />
          );
        })}
        {stream.text && (
          <p className="stream-text">
            {stream.text}
            <span className="stream-cursor" />
          </p>
        )}
        {!stream.text && tools.length === 0 && (
          <span className="stream-cursor" />
        )}
        {stream.usage && (
          <p className="usage">
            tokens: {stream.usage.total_tokens ?? "—"}
            {stream.finish ? ` · ${stream.finish}` : ""}
          </p>
        )}
      </div>
    </article>
  );
}
