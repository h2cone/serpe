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
    <div className="flex h-full flex-col items-center justify-center gap-3 p-8 text-center">
      <h1 className="text-2xl font-semibold">serpe</h1>
      <p className="max-w-md text-sm text-slate-400">
        Interactive agent shell. Create a session from the sidebar, or ensure
        <code className="mx-1 rounded bg-slate-800 px-1">serpeserve</code>
        is running on :8080.
      </p>
      <button
        type="button"
        className="text-sm text-sky-400 underline"
        onClick={() => revalidator.revalidate()}
      >
        Retry
      </button>
    </div>
  );
}
