import { Form, Link, Outlet, useLoaderData, useNavigation } from "react-router";
import type { Route } from "./+types/_layout";
import { api } from "~/lib/api";
import type { SessionSummary } from "~/lib/types";

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

export async function action({ request }: Route.ActionArgs) {
  const form = await request.formData();
  const intent = String(form.get("intent") ?? "create");
  if (intent === "create") {
    const created = await api.createSession();
    return Response.redirect(`/sessions/${created.id}`, 303);
  }
  return null;
}

export default function Layout() {
  const { sessions, error } = useLoaderData<typeof loader>();
  const nav = useNavigation();
  const busy = nav.state !== "idle";

  return (
    <div className="shell h-full">
      <aside className="sidebar flex flex-col gap-3 bg-slate-950/80">
        <div className="flex items-center justify-between gap-2">
          <Link to="/" className="text-sm font-semibold tracking-wide text-sky-300">
            serpe
          </Link>
          <Form method="post">
            <input type="hidden" name="intent" value="create" />
            <button
              type="submit"
              disabled={busy}
              className="rounded-md bg-sky-600 px-2 py-1 text-xs font-medium text-white hover:bg-sky-500 disabled:opacity-50"
            >
              New
            </button>
          </Form>
        </div>
        {error && (
          <p className="text-xs text-amber-400">API offline: {error}</p>
        )}
        <SessionList sessions={sessions} />
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
      <p className="text-xs text-slate-500">No sessions yet. Create one.</p>
    );
  }
  return (
    <ul className="flex flex-col gap-1">
      {sessions.map((s) => (
        <li key={s.id}>
          <Link
            to={`/sessions/${s.id}`}
            className="block rounded-md px-2 py-1.5 text-sm hover:bg-slate-800"
            prefetch="intent"
          >
            <div className="truncate font-medium text-slate-100">
              {s.title || s.preview || s.id}
            </div>
            <div className="truncate text-[11px] text-slate-500">
              {s.message_count} msgs · {s.id.slice(0, 8)}
            </div>
          </Link>
        </li>
      ))}
    </ul>
  );
}
