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
import type { Message, SessionSummary } from "~/lib/types";
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
      <div className="min-h-0 flex-1 overflow-y-auto p-4">
        <MessageList state={state} />
        <div ref={bottomRef} />
      </div>
      {state.error && (
        <div className="border-t border-red-900/50 bg-red-950/40 px-4 py-2 text-xs text-red-200">
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
    <div className="mx-auto flex max-w-3xl flex-col gap-4">
      {state.messages.map((m, i) => (
        <MessageBubble key={i} message={m} />
      ))}
      {state.stream && <StreamingAssistant stream={state.stream} />}
    </div>
  );
}

function MessageBubble({ message }: { message: Message }) {
  const isUser = message.role === "user";
  return (
    <div className={`flex ${isUser ? "justify-end" : "justify-start"}`}>
      <div
        className={`max-w-[90%] rounded-2xl px-3 py-2 text-sm leading-relaxed ${
          isUser
            ? "bg-sky-700 text-white"
            : "bg-slate-900 text-slate-100 ring-1 ring-slate-800"
        }`}
      >
        {message.content.map((p, i) => (
          <ContentPartView key={i} part={p} />
        ))}
      </div>
    </div>
  );
}

function StreamingAssistant({ stream }: { stream: StreamingState }) {
  const tools = Object.values(stream.tools);
  return (
    <div className="flex justify-start">
      <div className="max-w-[90%] rounded-2xl bg-slate-900 px-3 py-2 text-sm ring-1 ring-sky-900/60">
        {stream.reasoning && (
          <details open className="mb-2 text-xs text-slate-400">
            <summary className="cursor-pointer">Thinking</summary>
            <p className="mt-1 whitespace-pre-wrap">{stream.reasoning}</p>
          </details>
        )}
        {stream.refusal && (
          <p className="mb-2 whitespace-pre-wrap text-amber-300">
            {stream.refusal}
          </p>
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
          <p className="whitespace-pre-wrap">
            {stream.text}
            <span className="ml-0.5 inline-block h-3 w-1.5 animate-pulse bg-sky-400 align-middle" />
          </p>
        )}
        {!stream.text && tools.length === 0 && (
          <span className="inline-block h-3 w-1.5 animate-pulse bg-sky-400" />
        )}
        {stream.usage && (
          <p className="mt-2 text-[10px] text-slate-500">
            tokens: {stream.usage.total_tokens ?? "—"}
            {stream.finish ? ` · ${stream.finish}` : ""}
          </p>
        )}
      </div>
    </div>
  );
}
