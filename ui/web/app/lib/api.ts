import { apiOrigin } from "../../api-origin";
import {
  decodeSessionDetail,
  decodeSessionMutation,
  decodeSessionSummaries,
  type SessionDetail,
  type SessionMutation,
  type SessionSummary,
} from "./wire";

/** Browser: same origin. SSR/dev: backend API (Vite proxies in browser only). */
function apiBase(): string {
  if (typeof window !== "undefined") return "";
  return apiOrigin();
}

async function json<T>(
  path: string,
  decode: (value: unknown) => T,
  init?: RequestInit,
): Promise<T> {
  const headers = new Headers(init?.headers);
  headers.set("Accept", "application/json");
  if (init?.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(`${apiBase()}${path}`, {
    ...init,
    headers,
    cache: "no-store",
    credentials: "omit",
    redirect: "error",
  });
  if (!res.ok) {
    throw new APIError(res.status, responseProblem(res.status));
  }
  if (res.status === 204) return undefined as T;
  return decode(await res.json());
}

export class APIError extends Error {
  constructor(public readonly status: number, message: string) {
    super(message);
    this.name = "APIError";
  }
}

function responseProblem(status: number): string {
  switch (status) {
    case 400:
      return "The server rejected the request.";
    case 404:
      return "The session no longer exists.";
    case 409:
      return "The session changed concurrently. Reload and try again.";
    case 413:
      return "The requested record is too large to display.";
    case 429:
      return "The server is rate-limiting requests. Try again shortly.";
    case 500:
    case 502:
    case 503:
      return "Can't reach the local server.";
    default:
      return `The server returned HTTP ${status}.`;
  }
}

function sessionPath(id: string): string {
  if (!/^[A-Za-z0-9._-]{1,128}$/.test(id) || id === "." || id === "..") {
    throw new Error("Invalid session ID");
  }
  return `/api/sessions/${id}`;
}

export const api = {
  listSessions: (): Promise<SessionSummary[]> =>
    json("/api/sessions", decodeSessionSummaries),
  createSession: (body?: { cwd?: string; title?: string }) =>
    json<SessionMutation>("/api/sessions", decodeSessionMutation, {
      method: "POST",
      body: JSON.stringify(body ?? {}),
    }),
  getSession: (
    id: string,
    options?: { before?: string; limit?: number; signal?: AbortSignal },
  ): Promise<SessionDetail> => {
    const query = new URLSearchParams();
    if (options?.before) query.set("before", options.before);
    if (options?.limit !== undefined) query.set("limit", String(options.limit));
    const suffix = query.size > 0 ? `?${query.toString()}` : "";
    return json(`${sessionPath(id)}${suffix}`, decodeSessionDetail, {
      signal: options?.signal,
    });
  },
  patchSession: (id: string, body: { title?: string }) =>
    json<SessionMutation>(sessionPath(id), decodeSessionMutation, {
      method: "PATCH",
      body: JSON.stringify(body),
    }),
  deleteSession: (id: string) =>
    json<void>(sessionPath(id), () => undefined, { method: "DELETE" }),
};
