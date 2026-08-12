import { Form, useLoaderData } from "react-router";
import type { Route } from "./+types/sessions.$id";
import { ChatView } from "~/components/chat-view";
import { TrashIcon } from "~/components/icons";
import { api } from "~/lib/api";
import type { SessionSummary } from "~/lib/wire";

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
    <div className="session-page">
      <SessionHeader meta={meta} />
      <ChatView meta={meta} initialMessages={messages} />
    </div>
  );
}

function SessionHeader({ meta }: { meta: SessionSummary }) {
  return (
    <header className="session-header">
      <div className="session-heading">
        <h1>{meta.title || meta.preview || meta.id}</h1>
        <p
          className="session-context"
          title={`Session ${meta.id} · ${meta.cwd}`}
          aria-label={`Session ${meta.id}, working directory ${meta.cwd}`}
        >
          <span className="session-id">{meta.id.slice(0, 12)}</span>
          <span className="context-separator" aria-hidden="true" />
          <span className="session-cwd">{meta.cwd}</span>
        </p>
      </div>
      <div className="header-actions">
        <Form
          method="post"
          onSubmit={(e) => {
            if (!confirm("Delete this session?")) e.preventDefault();
          }}
        >
          <input type="hidden" name="intent" value="delete" />
          <button
            type="submit"
            className="quiet-button danger"
            aria-label="Delete session"
          >
            <TrashIcon className="button-icon" />
            <span className="button-label">Delete</span>
          </button>
        </Form>
      </div>
    </header>
  );
}
