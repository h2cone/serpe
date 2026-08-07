import { Form, useLoaderData } from "react-router";
import type { Route } from "./+types/sessions.$id";
import { ChatView } from "~/components/chat-view";
import { api } from "~/lib/api";
import type { SessionSummary } from "~/lib/types";

export async function loader({ params }: Route.LoaderArgs) {
  const id = params.id!;
  // Single GET: split meta/messages in-process (no dual full-detail fetch).
  const session = await api.getSession(id);
  const { messages, ...meta } = session;
  return { meta, messages };
}

export async function action({ request, params }: Route.ActionArgs) {
  const id = params.id!;
  const form = await request.formData();
  const intent = String(form.get("intent") ?? "");
  if (intent === "delete") {
    await api.deleteSession(id);
    return Response.redirect("/", 303);
  }
  if (intent === "fork") {
    const forked = await api.forkSession(id);
    return Response.redirect(`/sessions/${forked.id}`, 303);
  }
  if (intent === "rename") {
    const title = String(form.get("title") ?? "");
    await api.patchSession(id, { title });
    return null;
  }
  return null;
}

export default function SessionRoute() {
  const { meta, messages } = useLoaderData<typeof loader>();
  return (
    <div className="flex h-full min-h-0 flex-col">
      <SessionHeader meta={meta} />
      <ChatView meta={meta} initialMessages={messages} />
    </div>
  );
}

function SessionHeader({ meta }: { meta: SessionSummary }) {
  return (
    <header className="flex items-center justify-between gap-2 border-b border-slate-800 px-4 py-2">
      <div className="min-w-0">
        <h1 className="truncate text-sm font-semibold">
          {meta.title || meta.preview || meta.id}
        </h1>
        <p className="truncate text-[11px] text-slate-500">
          {meta.id} · {meta.cwd}
        </p>
      </div>
      <div className="flex gap-1">
        <Form method="post">
          <input type="hidden" name="intent" value="fork" />
          <button
            type="submit"
            className="rounded px-2 py-1 text-xs text-slate-300 hover:bg-slate-800"
          >
            Fork
          </button>
        </Form>
        <Form
          method="post"
          onSubmit={(e) => {
            if (!confirm("Delete this session?")) e.preventDefault();
          }}
        >
          <input type="hidden" name="intent" value="delete" />
          <button
            type="submit"
            className="rounded px-2 py-1 text-xs text-red-300 hover:bg-slate-800"
          >
            Delete
          </button>
        </Form>
      </div>
    </header>
  );
}
