import { useCallback, useEffect, useRef, useState } from "react";
import { useParams, useRevalidator } from "react-router";
import { api } from "~/lib/api";
import { streamRun } from "~/lib/sse";
import {
  appendUser,
  applyFrame,
  failStream,
  initialTranscript,
  prependMessages,
  type StreamingState,
  type TranscriptState,
} from "~/lib/transcript";
import type { Message, SessionSummary } from "~/lib/wire";
import { pendingPromptKey } from "~/lib/prefs";
import { Composer } from "./composer";
import { ContentPartView, ToolCallCard } from "./content-part";

const startedPending = new Set<string>();

export function ChatView({
  meta,
  initialMessages,
  initialMessageStart,
  initialSnapshotLength,
  initialNextBefore,
}: {
  meta: SessionSummary;
  initialMessages: Message[];
  initialMessageStart: number;
  initialSnapshotLength: number;
  initialNextBefore?: string;
}) {
  const revalidator = useRevalidator();
  const params = useParams();
  const [state, setState] = useState<TranscriptState>(() =>
    initialTranscript(initialMessages, initialMessageStart),
  );
  const [nextBefore, setNextBefore] = useState(initialNextBefore);
  const [snapshotLength, setSnapshotLength] = useState(initialSnapshotLength);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [olderError, setOlderError] = useState<string | null>(null);
  const [runNotice, setRunNotice] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const olderAbortRef = useRef<AbortController | null>(null);
  const pageGenerationRef = useRef(0);
  const scrollRef = useRef<HTMLDivElement>(null);
  const bottomRef = useRef<HTMLDivElement>(null);

  // Reset when session changes or loader revalidates messages.
  useEffect(() => {
    pageGenerationRef.current++;
    olderAbortRef.current?.abort();
    setState(initialTranscript(initialMessages, initialMessageStart));
    setNextBefore(initialNextBefore);
    setSnapshotLength(initialSnapshotLength);
    setLoadingOlder(false);
    setOlderError(null);
  }, [
    meta.id,
    initialMessages,
    initialMessageStart,
    initialNextBefore,
    initialSnapshotLength,
  ]);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: "end" });
  }, [meta.id, initialMessages]);

  useEffect(() => {
    if (state.streaming) {
      bottomRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
    }
  }, [state.streaming, state.stream]);

  useEffect(
    () => () => {
      abortRef.current?.abort();
      olderAbortRef.current?.abort();
    },
    [],
  );

  const send = useCallback(
    async (prompt: string) => {
      if (!prompt.trim() || state.streaming) return false;
      const ac = new AbortController();
      abortRef.current = ac;
      setRunNotice(null);
      setState((s) => appendUser(s, prompt));
      try {
        let terminalIssue: string | null = null;
        for await (const frame of streamRun(
          { session_id: meta.id, prompt },
          ac.signal,
        )) {
          setState((s) => applyFrame(s, frame));
          if (frame.t === "error") terminalIssue = frame.message;
        }
        if (terminalIssue) setRunNotice(terminalIssue);
        revalidator.revalidate();
        return true;
      } catch (e) {
        if ((e as Error).name === "AbortError") {
          const message = "Run cancelled. The session was reloaded to verify its committed state.";
          setRunNotice(message);
          setState((s) => failStream(s, message, "cancelled"));
          revalidator.revalidate();
          return false;
        }
        const message =
          e instanceof Error ? e.message : "The run stream failed.";
        setRunNotice(message);
        setState((s) => failStream(s, message));
        revalidator.revalidate();
        return true;
      } finally {
        if (abortRef.current === ac) abortRef.current = null;
      }
    },
    [meta.id, state.streaming, revalidator],
  );

  useEffect(() => {
    const key = pendingPromptKey(meta.id);
    const pending = sessionStorage.getItem(key);
    if (!pending || startedPending.has(meta.id)) return;
    startedPending.add(meta.id);
    void send(pending).then((ok) => {
      if (ok) sessionStorage.removeItem(key);
      else startedPending.delete(meta.id);
    });
  }, [meta.id, send]);

  const stop = () => abortRef.current?.abort();

  const loadOlder = useCallback(async () => {
    if (!nextBefore || loadingOlder || state.streaming) return;
    const cursor = nextBefore;
    const expectedStart = state.messageStart;
    const expectedSnapshot = snapshotLength;
    const generation = pageGenerationRef.current;
    const scroller = scrollRef.current;
    const oldHeight = scroller?.scrollHeight ?? 0;
    const oldTop = scroller?.scrollTop ?? 0;
    const ac = new AbortController();
    olderAbortRef.current = ac;
    setLoadingOlder(true);
    setOlderError(null);
    try {
      const page = await api.getSession(meta.id, {
        before: cursor,
        limit: 100,
        signal: ac.signal,
      });
      if (generation !== pageGenerationRef.current) return;
      if (
        page.snapshot_length !== expectedSnapshot ||
        page.message_start + page.messages.length !== expectedStart
      ) {
        throw new Error("The session history snapshot changed. Reload and try again.");
      }
      setState((current) =>
        prependMessages(current, page.messages, page.message_start),
      );
      setNextBefore(page.next_before);
      requestAnimationFrame(() => {
        if (!scroller) return;
        scroller.scrollTop = oldTop + (scroller.scrollHeight - oldHeight);
      });
    } catch (error) {
      if ((error as Error).name !== "AbortError") {
        setOlderError(
          error instanceof Error
            ? error.message
            : "Older messages could not be loaded.",
        );
      }
    } finally {
      if (generation === pageGenerationRef.current) setLoadingOlder(false);
      if (olderAbortRef.current === ac) olderAbortRef.current = null;
    }
  }, [
    loadingOlder,
    meta.id,
    nextBefore,
    snapshotLength,
    state.messageStart,
    state.streaming,
  ]);

  const visibleError = runNotice ?? state.error;

  return (
    <>
      <div className="chat-scroll" ref={scrollRef}>
        {nextBefore && (
          <div className="history-control">
            <button
              type="button"
              className="load-older-button"
              onClick={loadOlder}
              disabled={loadingOlder || state.streaming}
              aria-busy={loadingOlder}
            >
              {loadingOlder ? "Loading earlier messages…" : "Load earlier messages"}
            </button>
            {olderError && (
              <p className="history-error" role="alert">
                {olderError}
              </p>
            )}
          </div>
        )}
        <MessageList state={state} />
        <div ref={bottomRef} />
      </div>
      {visibleError && (
        <div className="error-banner" role="alert">
          {visibleError}
          {state.stop ? ` (${state.stop})` : ""}
        </div>
      )}
      <Composer
        streaming={state.streaming}
        onSend={(prompt) => {
          void send(prompt);
        }}
        onStop={stop}
        cwd={meta.cwd}
        autoFocus
        key={params.id}
      />
    </>
  );
}

function MessageList({ state }: { state: TranscriptState }) {
  return (
    <div className="message-list">
      {state.messages.map((m, i) => (
        <MessageBubble key={state.messageStart + i} message={m} />
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
  const tools = Array.from(stream.tools.values()).sort(
    (left, right) =>
      left.turn - right.turn ||
      (left.index ?? Number.MAX_SAFE_INTEGER) -
        (right.index ?? Number.MAX_SAFE_INTEGER) ||
      (left.part ?? Number.MAX_SAFE_INTEGER) -
        (right.part ?? Number.MAX_SAFE_INTEGER),
  );
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
              key={`${t.turn}:${t.id ?? t.index ?? t.part ?? t.name}`}
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
