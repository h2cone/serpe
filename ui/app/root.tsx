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

export const links: Route.LinksFunction = () => [];

export function Layout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <head>
        <meta charSet="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        <Meta />
        <Links />
        {/* Critical shell CSS for FCP before full stylesheet applies */}
        <style
          dangerouslySetInnerHTML={{
            __html: `
              html,body{height:100%;margin:0;background:#0b0f14;color:#e8eef5;
              font-family:ui-sans-serif,system-ui,sans-serif}
              .shell{display:flex;height:100%}
              .sidebar{width:16rem;border-right:1px solid #1e293b;padding:0.75rem;overflow:auto}
              .main{flex:1;display:flex;flex-direction:column;min-width:0}
            `,
          }}
        />
      </head>
      <body>
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

export function ErrorBoundary({ error }: Route.ErrorBoundaryProps) {
  let message = "Oops!";
  let details = "An unexpected error occurred.";
  if (isRouteErrorResponse(error)) {
    message = error.status === 404 ? "404" : "Error";
    details =
      error.status === 404
        ? "The requested page could not be found."
        : error.statusText || details;
  } else if (import.meta.env.DEV && error && error instanceof Error) {
    details = error.message;
  }
  return (
    <main className="p-8">
      <h1 className="text-xl font-semibold">{message}</h1>
      <p className="mt-2 text-slate-400">{details}</p>
    </main>
  );
}
