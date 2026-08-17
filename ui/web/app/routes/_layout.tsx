import { useEffect, useState } from "react";
import {
  Link,
  NavLink,
  Outlet,
  useLoaderData,
  useLocation,
  useRevalidator,
} from "react-router";
import { api } from "~/lib/api";
import type { SessionSummary } from "~/lib/wire";
import { MenuIcon, PlusIcon } from "~/components/icons";

export async function clientLoader() {
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
  const { sessions, error } = useLoaderData<typeof clientLoader>();
  const location = useLocation();
  const revalidator = useRevalidator();
  const [railOpen, setRailOpen] = useState(false);
  const onHome = location.pathname === "/";

  useEffect(() => {
    setRailOpen(false);
  }, [location.pathname]);

  useEffect(() => {
    if (!railOpen) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") setRailOpen(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [railOpen]);

  return (
    <div className={`shell${railOpen ? " is-rail-open" : ""}`}>
      <button
        type="button"
        className="rail-backdrop"
        aria-label="Close sidebar"
        tabIndex={railOpen ? 0 : -1}
        onClick={() => setRailOpen(false)}
      />
      <aside className="rail" aria-label="Sidebar">
        <div className="rail-top">
          <Link to="/" className="brand" aria-label="Serpe home">
            <span className="brand-wordmark">Serpe</span>
          </Link>
        </div>
        <Link
          to="/"
          className={`new-chat${onHome ? " is-active" : ""}`}
          aria-current={onHome ? "page" : undefined}
          title="New chat"
        >
          <PlusIcon className="button-icon" />
          <span>New chat</span>
        </Link>
        <nav className="session-nav" aria-label="Recents">
          <div className="session-nav-heading">Recents</div>
          <SessionList sessions={sessions} />
        </nav>
        <div className="rail-foot">
          <button
            type="button"
            className="presence-button"
            title={error ? `${error} Click to retry.` : "Connected to the local server"}
            onClick={() => revalidator.revalidate()}
          >
            <span className="presence">{error ? "Offline" : "Local"}</span>
          </button>
        </div>
      </aside>
      <main className="stage">
        <div className="stage-chrome">
          <button
            type="button"
            className="menu-button"
            aria-label="Open sidebar"
            aria-expanded={railOpen}
            onClick={() => setRailOpen(true)}
          >
            <MenuIcon className="button-icon" />
          </button>
        </div>
        <Outlet context={{ defaultCwd: sessions[0]?.cwd ?? "" }} />
      </main>
    </div>
  );
}

function SessionList({ sessions }: { sessions: SessionSummary[] }) {
  if (sessions.length === 0) {
    return <p className="session-empty">No sessions yet</p>;
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
            title={s.cwd}
          >
            <div className="session-title">{s.title || s.preview || s.id}</div>
          </NavLink>
        </li>
      ))}
    </ul>
  );
}
