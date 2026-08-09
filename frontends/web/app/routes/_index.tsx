import { redirect, useLoaderData, useRevalidator } from "react-router";
import { api } from "~/lib/api";

export async function loader() {
  try {
    const sessions = await api.listSessions();
    if (sessions.length > 0) {
      return redirect(`/sessions/${sessions[0].id}`);
    }
  } catch {
    // API down: show landing
  }
  return { empty: true };
}

export default function Index() {
  useLoaderData<typeof loader>();
  const revalidator = useRevalidator();
  return (
    <div className="empty-state">
      <div className="empty-state-inner">
        <div className="empty-mark" aria-hidden="true" />
        <h1>Ready when you are</h1>
        <p>
          Create a session from the sidebar, or make sure <code>serpe-server</code>{" "}
          is running on :8080.
        </p>
        <button
          type="button"
          className="retry-button"
          onClick={() => revalidator.revalidate()}
        >
          Retry connection
        </button>
      </div>
    </div>
  );
}
