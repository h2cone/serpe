import type { ContentPart, Message } from "~/lib/wire";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { ToolIcon } from "./icons";

export function ContentPartView({ part }: { part: ContentPart }) {
  switch (part.type) {
    case "text":
      return <MarkdownText text={part.text} />;
    case "reasoning_summary":
      return (
        <details className="thinking">
          <summary>Thinking</summary>
          <p>{part.text}</p>
        </details>
      );
    case "refusal":
      return <p className="refusal">{part.text}</p>;
    case "tool_call":
      return (
        <ToolCallCard name={part.name} args={part.arguments} status="done" />
      );
    case "tool_result":
      return (
        <section className={`result-card${part.is_error ? " error" : ""}`}>
          <div className="result-heading">
            <span className="tool-glyph" aria-hidden="true">
              <ToolIcon className="tool-icon" />
            </span>
            <span className="result-title">
              <span className="result-label">Tool output</span>
              <span className="result-name">{part.name}</span>
            </span>
            <span className="result-status">
              <span className="status-dot" aria-hidden="true" />
              {part.is_error ? "Failed" : "Complete"}
            </span>
          </div>
          <div className="result-content">
            {part.content.map((c, i) => (
              <ContentPartView key={i} part={c} />
            ))}
          </div>
        </section>
      );
    case "image": {
      const src =
        part.uri ||
        (part.data && part.mime
          ? `data:${part.mime};base64,${part.data}`
          : undefined);
      if (!src) return null;
      return (
        <img src={src} alt="" className="content-image" />
      );
    }
    default:
      return null;
  }
}

function MarkdownText({ text }: { text: string }) {
  return (
    <div className="content-markdown">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          a: ({ node: _node, ...props }) => (
            <a {...props} target="_blank" rel="noreferrer" />
          ),
        }}
      >
        {text}
      </ReactMarkdown>
    </div>
  );
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
    <section className={`tool-card${result?.is_error ? " error" : ""}`}>
      <div className="tool-heading">
        <span className="tool-glyph" aria-hidden="true">
          <ToolIcon className="tool-icon" />
        </span>
        <span className="tool-name">{name}</span>
        <span className="tool-status">
          <span
            className={`status-dot${status === "running" ? " is-running" : ""}`}
            aria-hidden="true"
          />
          {status === "running" ? "Running" : "Complete"}
        </span>
      </div>
      {argsDisplay && <pre className="tool-args">{argsDisplay}</pre>}
      {result && (
        <div className={`tool-result${result.is_error ? " error" : ""}`}>
          {result.content.map((c, i) => (
            <ContentPartView key={i} part={c} />
          ))}
        </div>
      )}
    </section>
  );
}
