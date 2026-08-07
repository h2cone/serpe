import type { ContentPart, Message } from "~/lib/types";

export function ContentPartView({ part }: { part: ContentPart }) {
  switch (part.type) {
    case "text":
      return <p className="whitespace-pre-wrap">{part.text}</p>;
    case "reasoning_summary":
      return (
        <details className="mb-2 text-xs text-slate-400">
          <summary className="cursor-pointer">Thinking</summary>
          <p className="mt-1 whitespace-pre-wrap">{part.text}</p>
        </details>
      );
    case "refusal":
      return (
        <p className="whitespace-pre-wrap text-amber-300">{part.text}</p>
      );
    case "tool_call":
      return (
        <ToolCallCard name={part.name} args={part.arguments} status="done" />
      );
    case "tool_result":
      return (
        <div
          className={`my-1 rounded-md border px-2 py-1 text-xs ${
            part.is_error
              ? "border-red-800 bg-red-950/40 text-red-200"
              : "border-slate-700 bg-slate-950 text-slate-300"
          }`}
        >
          <div className="font-mono text-[11px] text-slate-500">
            result · {part.name}
          </div>
          {part.content.map((c, i) => (
            <ContentPartView key={i} part={c} />
          ))}
        </div>
      );
    case "image": {
      const src =
        part.uri ||
        (part.data && part.mime
          ? `data:${part.mime};base64,${part.data}`
          : undefined);
      if (!src) return null;
      return (
        <img src={src} alt="" className="my-1 max-h-64 rounded-md" />
      );
    }
    default:
      return null;
  }
}

export function ToolCallCard({
  name,
  args,
  status,
  result,
  argsText,
}: {
  name: string;
  args?: Record<string, unknown>;
  status: "running" | "done";
  result?: { content: Message["content"]; is_error?: boolean };
  /** Raw streaming args when not yet parsed as object. */
  argsText?: string;
}) {
  const argsDisplay =
    args && Object.keys(args).length > 0
      ? JSON.stringify(args, null, 2)
      : argsText || null;

  return (
    <div
      className={`my-1 rounded-md border px-2 py-1.5 text-xs ${
        result?.is_error
          ? "border-red-800 bg-red-950/30"
          : "border-violet-900/50 bg-violet-950/20"
      }`}
    >
      <div className="flex items-center gap-2 font-mono">
        <span className="text-violet-300">{name}</span>
        <span className="text-slate-500">
          {status === "running" ? "running…" : "done"}
        </span>
      </div>
      {argsDisplay && (
        <pre className="mt-1 overflow-x-auto text-[11px] text-slate-400">
          {argsDisplay}
        </pre>
      )}
      {result && (
        <div
          className={`mt-1 border-t border-slate-800 pt-1 ${
            result.is_error ? "text-red-200" : "text-slate-300"
          }`}
        >
          {result.content.map((c, i) => (
            <ContentPartView key={i} part={c} />
          ))}
        </div>
      )}
    </div>
  );
}
