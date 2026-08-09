import { useEffect, useRef, useState } from "react";
import { SendIcon, StopIcon } from "./icons";

export function Composer({
  streaming,
  onSend,
  onStop,
}: {
  streaming: boolean;
  onSend: (prompt: string) => void;
  onStop: () => void;
}) {
  const [value, setValue] = useState("");
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const canSend = value.trim().length > 0 && !streaming;

  useEffect(() => {
    const textarea = textareaRef.current;
    if (!textarea) return;
    textarea.style.height = "auto";
    textarea.style.height = `${Math.min(textarea.scrollHeight, 160)}px`;
  }, [value]);

  return (
    <form
      className="composer-shell"
      onSubmit={(e) => {
        e.preventDefault();
        const v = value.trim();
        if (!v || streaming) return;
        setValue("");
        onSend(v);
      }}
    >
      <div className="composer-box">
        <textarea
          ref={textareaRef}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              e.currentTarget.form?.requestSubmit();
            }
          }}
          rows={1}
          disabled={streaming}
          aria-label="Message"
          placeholder={streaming ? "serpe is responding…" : "Message serpe…"}
          className="composer-textarea"
        />
        <div className="composer-footer">
          <span className="composer-hint">Enter to send · Shift + Enter for a new line</span>
          {streaming ? (
            <button
              type="button"
              onClick={onStop}
              className="stop-button"
              aria-label="Stop response"
              title="Stop response"
            >
              <StopIcon className="button-icon" />
            </button>
          ) : (
            <button
              type="submit"
              className="send-button"
              disabled={!canSend}
              aria-label="Send message"
              title="Send message"
            >
              <SendIcon className="button-icon" />
            </button>
          )}
        </div>
      </div>
    </form>
  );
}
