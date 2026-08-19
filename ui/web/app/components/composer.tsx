import { useEffect, useRef, useState } from "react";
import { FolderIcon, SendIcon, StopIcon } from "./icons";
import { shortPath } from "~/lib/prefs";

export function Composer({
  streaming,
  onSend,
  onStop,
  cwd,
  cwdEditable = false,
  cwdPicking = false,
  onPickCwd,
  disabled = false,
  placeholder,
  autoFocus = false,
}: {
  streaming: boolean;
  onSend: (prompt: string) => void;
  onStop: () => void;
  cwd?: string;
  cwdEditable?: boolean;
  cwdPicking?: boolean;
  onPickCwd?: () => void;
  disabled?: boolean;
  placeholder?: string;
  autoFocus?: boolean;
}) {
  const [value, setValue] = useState("");
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const busy = disabled || streaming;
  const canSend = value.trim().length > 0 && !busy;
  const folderLocked = !cwdEditable || busy || cwdPicking;

  useEffect(() => {
    const textarea = textareaRef.current;
    if (!textarea) return;
    textarea.style.height = "auto";
    textarea.style.height = `${Math.max(34, Math.min(textarea.scrollHeight, 160))}px`;
  }, [value]);

  const hasFolder = Boolean(cwd?.trim());
  const cwdLabel = hasFolder ? shortPath(cwd!.trim()) : "";

  return (
    <form
      className="composer-dock"
      onSubmit={(e) => {
        e.preventDefault();
        const next = value.trim();
        if (!next || busy) return;
        setValue("");
        onSend(next);
      }}
    >
      <div className="composer">
        <button
          type="button"
          className={`cwd-chip${hasFolder ? "" : " is-icon"}${cwdPicking ? " is-picking" : ""}`}
          disabled={folderLocked}
          aria-busy={cwdPicking}
          title={
            cwdPicking
              ? "Opening the folder picker…"
              : hasFolder
                ? cwd
                : "Choose a working folder from the file browser"
          }
          aria-label={
            cwdEditable
              ? hasFolder
                ? `Working folder ${cwdLabel}. Click to choose another folder.`
                : "Choose a working folder"
              : `Working folder ${cwdLabel}`
          }
          onClick={() => {
            if (folderLocked) return;
            onPickCwd?.();
          }}
        >
          <FolderIcon className="button-icon" />
          {hasFolder && <span className="cwd-chip-path">{cwdLabel}</span>}
        </button>
        <textarea
          ref={textareaRef}
          data-composer="true"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              e.currentTarget.form?.requestSubmit();
            }
          }}
          rows={1}
          disabled={busy}
          autoFocus={autoFocus}
          aria-label="Message"
          placeholder={
            placeholder ??
            (streaming ? "Serpe is responding…" : "What should we work on?")
          }
          className="composer-textarea"
        />
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
    </form>
  );
}
