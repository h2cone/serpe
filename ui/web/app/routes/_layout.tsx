import {
  Form,
  Link,
  NavLink,
  Outlet,
  useLoaderData,
  useNavigation,
  useRevalidator,
} from "react-router";
import { useState, useSyncExternalStore } from "react";
import { api } from "~/lib/api";
import {
  authSnapshot,
  clearAPIToken,
  isAuthRequired,
  serverAuthSnapshot,
  setAPIToken,
  subscribeAuth,
} from "~/lib/auth";
import type { SessionSummary } from "~/lib/wire";
import { BrandMark, PlusIcon } from "~/components/icons";

export async function clientLoader() {
  try {
    const sessions = await api.listSessions();
    return { sessions, error: null as string | null };
  } catch (e) {
    if (isAuthRequired(e)) {
      return { sessions: [] as SessionSummary[], error: null as string | null };
    }
    return {
      sessions: [] as SessionSummary[],
      error: e instanceof Error ? e.message : "failed to load sessions",
    };
  }
}

export default function Layout() {
  const { sessions, error } = useLoaderData<typeof clientLoader>();
  const nav = useNavigation();
  const revalidator = useRevalidator();
  const auth = useSyncExternalStore(
    subscribeAuth,
    authSnapshot,
    serverAuthSnapshot,
  );
  const busy = nav.state !== "idle";

  if (!auth.token) {
    return (
      <TokenGate
        issue={auth.issue}
        onUnlock={() => revalidator.revalidate()}
      />
    );
  }

  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand-row">
          <Link to="/" className="brand" aria-label="serpe home">
            <BrandMark className="brand-mark" />
            <span className="brand-wordmark">serpe</span>
          </Link>
          <Form method="post" action="/sessions/new">
            <button
              type="submit"
              disabled={busy}
              className="new-button"
              aria-busy={busy}
            >
              <PlusIcon className="button-icon" />
              {busy ? "Creating…" : "New"}
            </button>
          </Form>
        </div>
        <button
          type="button"
          className="lock-button"
          onClick={clearAPIToken}
        >
          Clear token
        </button>
        {error && (
          <p className="api-error">API offline: {error}</p>
        )}
        <nav className="session-nav" aria-label="Sessions">
          <div className="session-nav-heading">
            <span>Sessions</span>
            <span>{sessions.length}</span>
          </div>
          <SessionList sessions={sessions} />
        </nav>
      </aside>
      <main className="main min-h-0">
        <Outlet />
      </main>
    </div>
  );
}

function TokenGate({
  issue,
  onUnlock,
}: {
  issue: string | null;
  onUnlock: () => void;
}) {
  const [value, setValue] = useState("");
  const [problem, setProblem] = useState<string | null>(null);

  return (
    <main className="token-gate">
      <section className="token-panel" aria-labelledby="token-heading">
        <BrandMark className="token-mark" />
        <h1 id="token-heading">Connect to Serpe</h1>
        <p>
          Enter the bearer token configured for this server. It stays only in
          this tab's memory and is cleared on refresh.
        </p>
        <form
          onSubmit={(event) => {
            event.preventDefault();
            try {
              setAPIToken(value);
              setValue("");
              setProblem(null);
              onUnlock();
            } catch (error) {
              setProblem(
                error instanceof Error ? error.message : "Token is invalid.",
              );
            }
          }}
        >
          <label htmlFor="api-token">API token</label>
          <input
            id="api-token"
            name="api-token"
            type="password"
            value={value}
            minLength={32}
            maxLength={4096}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            required
            autoFocus
            onChange={(event) => setValue(event.currentTarget.value)}
            aria-describedby="token-memory token-problem"
          />
          <p id="token-memory" className="token-memory">
            The token is never stored in a URL, cookie, or browser storage.
          </p>
          {(problem || issue) && (
            <p id="token-problem" className="token-problem" role="alert">
              {problem ?? issue}
            </p>
          )}
          <button type="submit" className="token-submit">
            Connect
          </button>
        </form>
      </section>
    </main>
  );
}

function SessionList({ sessions }: { sessions: SessionSummary[] }) {
  if (sessions.length === 0) {
    return (
      <p className="session-empty">No sessions yet. Create one.</p>
    );
  }
  return (
    <ul className="session-list">
      {sessions.map((s) => (
        <li key={s.id}>
          <NavLink
            to={`/sessions/${s.id}`}
            className={({ isActive }) =>
              `session-link${isActive ? " is-active" : ""}`
            }
            prefetch="intent"
          >
            <div className="session-title">{s.title || s.preview || s.id}</div>
            <div className="session-meta">
              {s.message_count} {s.message_count === 1 ? "message" : "messages"}
              <span aria-hidden="true"> · </span>
              {s.id.slice(0, 8)}
            </div>
          </NavLink>
        </li>
      ))}
    </ul>
  );
}
