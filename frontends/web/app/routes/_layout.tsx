import {
  Form,
  Link,
  NavLink,
  Outlet,
  useLoaderData,
  useNavigation,
} from "react-router";
import { api } from "~/lib/api";
import type { SessionSummary } from "~/lib/wire";
import { BrandMark, PlusIcon } from "~/components/icons";

export async function loader() {
  try {
    const sessions = await api.listSessions();
    return { sessions, error: null as string | null };
  } catch (e) {
    return {
      sessions: [] as SessionSummary[],
      error: e instanceof Error ? e.message : "failed to load sessions",
    };
  }
}

export default function Layout() {
  const { sessions, error } = useLoaderData<typeof loader>();
  const nav = useNavigation();
  const busy = nav.state !== "idle";

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
