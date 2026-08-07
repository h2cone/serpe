import { useState } from "react";

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
  return (
    <form
      className="border-t border-slate-800 p-3"
      onSubmit={(e) => {
        e.preventDefault();
        const v = value.trim();
        if (!v || streaming) return;
        setValue("");
        onSend(v);
      }}
    >
      <div className="mx-auto flex max-w-3xl gap-2">
        <textarea
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              e.currentTarget.form?.requestSubmit();
            }
          }}
          rows={2}
          disabled={streaming}
          placeholder={streaming ? "Streaming…" : "Message (Enter to send)"}
          className="min-h-[2.5rem] flex-1 resize-none rounded-xl border border-slate-700 bg-slate-900 px-3 py-2 text-sm outline-none ring-sky-600 focus:ring-1 disabled:opacity-60"
        />
        {streaming ? (
          <button
            type="button"
            onClick={onStop}
            className="rounded-xl bg-slate-700 px-4 text-sm font-medium hover:bg-slate-600"
          >
            Stop
          </button>
        ) : (
          <button
            type="submit"
            className="rounded-xl bg-sky-600 px-4 text-sm font-medium text-white hover:bg-sky-500"
          >
            Send
          </button>
        )}
      </div>
    </form>
  );
}
