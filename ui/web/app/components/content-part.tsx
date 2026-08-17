import { useEffect, useRef, useState } from "react";
import type { StreamToolStatus } from "~/lib/transcript";
import type { ContentPart, Message } from "~/lib/wire";
export function ContentPartView({ part }: { part: ContentPart }) {
  switch (part.type) {
    case "text":
      return <p className="content-text">{part.text}</p>;
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
            <span className="result-name">{part.name}</span>
            <span className="result-status">
              {part.is_error ? "Failed" : "Done"}
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
      return <ContentImage part={part} />;
    }
    default:
      return null;
  }
}

const inlineImageMIMEs = new Set([
  "image/png",
  "image/jpeg",
  "image/gif",
  "image/webp",
]);
const maxInlineImageBytes = 7 << 20;
const canonicalBase64 =
  /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/;

function ContentImage({
  part,
}: {
  part: Extract<ContentPart, { type: "image" }>;
}) {
  const [src, setSrc] = useState<string | null>(null);
  const objectURL = useRef<string | null>(null);

  const revoke = () => {
    if (!objectURL.current) return;
    URL.revokeObjectURL(objectURL.current);
    objectURL.current = null;
  };

  useEffect(() => {
    revoke();
    setSrc(null);
    if (part.data && part.mime && inlineImageMIMEs.has(part.mime)) {
      const estimatedBytes = Math.floor((part.data.length * 3) / 4);
      if (
        part.data.length > 0 &&
        part.data.length % 4 === 0 &&
        estimatedBytes <= maxInlineImageBytes &&
        canonicalBase64.test(part.data)
      ) {
        try {
          const binary = atob(part.data);
          if (binary.length <= maxInlineImageBytes) {
            const bytes = new Uint8Array(binary.length);
            for (let index = 0; index < binary.length; index++) {
              bytes[index] = binary.charCodeAt(index);
            }
            const url = URL.createObjectURL(
              new Blob([bytes], { type: part.mime }),
            );
            objectURL.current = url;
            setSrc(url);
          }
        } catch {
          // Invalid media remains an explicit unavailable state below.
        }
      }
    } else if (part.uri) {
      try {
        const url = new URL(part.uri, window.location.href);
        if (
          url.origin === window.location.origin &&
          (url.protocol === "http:" || url.protocol === "https:")
        ) {
          setSrc(url.href);
        }
      } catch {
        // Never initiate a request for an invalid or cross-origin URI.
      }
    }
    return revoke;
  }, [part.data, part.mime, part.uri]);

  if (!src) {
    return <p className="image-unavailable">Image unavailable.</p>;
  }
  return (
    <img
      src={src}
      alt="Tool result"
      className="content-image"
      onLoad={revoke}
      onError={revoke}
    />
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
  status: StreamToolStatus;
  result?: { content: Message["content"]; is_error?: boolean };
  /** Raw streaming args when not yet parsed as object. */
  argsText?: string;
}) {
  const failed = status === "failed" || result?.is_error === true;
  const statusLabel =
    status === "pending"
      ? "Queued"
      : status === "running"
        ? "Running"
        : failed
          ? "Failed"
          : "Complete";
  const argsDisplay =
    args && Object.keys(args).length > 0
      ? JSON.stringify(args, null, 2)
      : argsText || null;

  return (
    <section className={`tool-card${failed ? " error" : ""}`}>
      <div className="tool-heading">
        <span className="tool-name">{name}</span>
        <span className="tool-status">{statusLabel}</span>
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
