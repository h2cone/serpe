import { useState } from "react";
import { useNavigate, useOutletContext } from "react-router";
import { Composer } from "~/components/composer";
import { api } from "~/lib/api";
import {
  loadWorkspace,
  pendingPromptKey,
  saveWorkspace,
  titleFromPrompt,
} from "~/lib/prefs";

export default function Index() {
  const navigate = useNavigate();
  const { defaultCwd } = useOutletContext<{ defaultCwd: string }>();
  const [cwd, setCwd] = useState(() => loadWorkspace() || defaultCwd);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  return (
    <div className="landing">
      <h1 className="landing-mark">Serpe</h1>
      <Composer
        streaming={false}
        disabled={busy}
        cwd={cwd}
        cwdEditable
        autoFocus
        placeholder="What should we work on?"
        onCwdChange={(next) => {
          setCwd(next);
          saveWorkspace(next);
        }}
        onStop={() => {}}
        onSend={async (prompt) => {
          setBusy(true);
          setError(null);
          try {
            const created = await api.createSession({
              title: titleFromPrompt(prompt),
              ...(cwd.trim() ? { cwd: cwd.trim() } : {}),
            });
            sessionStorage.setItem(pendingPromptKey(created.id), prompt);
            navigate(`/sessions/${created.id}`);
          } catch (e) {
            setError(
              e instanceof Error ? e.message : "Could not create a session.",
            );
            setBusy(false);
          }
        }}
      />
      {error && (
        <p className="landing-error" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
