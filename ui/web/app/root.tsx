import {
  Links,
  Meta,
  Outlet,
  Scripts,
  ScrollRestoration,
  isRouteErrorResponse,
} from "react-router";
import type { Route } from "./+types/root";
import "./app.css";

const directionContract = `THESIS: Serpe is a local agent field — one word, one pill, a rail of recents. It refuses the boxed admin console and the filled teal-bubble kit.
OWN-WORLD: Paper-white field, pale rail, hairline seams, near-black circular send, one small teal mark. System sans. Soft offset shadow only on the pill.
STORY: A developer types on home and is inside a CWD-bound session. History is titles. Local is visible. Tools appear in the conversation.
FIRST VIEWPORT: Narrow rail (Serpe, New chat, Recents, Local). Vast white stage. Centered Serpe. Slim pill (folder, input, send).
FORM: Category canon, Field landing after SuperGrok. Standing exit; craft bar ChatGPT / SuperGrok / DeepSeek Harness. No concept-seed key. FIELD-CANON-2026
FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, and DESIGN.md`;

export const links: Route.LinksFunction = () => [];

export function Layout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <head>
        <meta charSet="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        <meta name="theme-color" content="#ffffff" />
        <Meta />
        <Links />
      </head>
      <body>
        <div
          aria-hidden="true"
          dangerouslySetInnerHTML={{
            __html: `<!--\n${directionContract}\n-->`,
          }}
          style={{ display: "none" }}
        />
        {children}
        <ScrollRestoration />
        <Scripts />
      </body>
    </html>
  );
}

export default function App() {
  return <Outlet />;
}

export function HydrateFallback() {
  return (
    <main className="loading-gate">
      <section className="loading-panel" aria-labelledby="loading-heading">
        <h1 id="loading-heading">Serpe</h1>
        <p>Opening the local interface…</p>
      </section>
    </main>
  );
}

export function ErrorBoundary({ error }: Route.ErrorBoundaryProps) {
  let message = "Something went wrong";
  let details = "The interface hit an unexpected error.";
  if (isRouteErrorResponse(error)) {
    message = error.status === 404 ? "Not found" : "Error";
    details =
      error.status === 404
        ? "That page is not in this interface."
        : error.statusText || details;
  } else if (import.meta.env.DEV && error && error instanceof Error) {
    details = error.message;
  }
  return (
    <main className="error-page">
      <div className="error-page-inner">
        <h1>{message}</h1>
        <p>{details}</p>
      </div>
    </main>
  );
}
