import { apiOrigin } from "../../api-origin";
import {
  decodeSessionDetail,
  decodeSessionSummaries,
  type SessionDetail,
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
  const res = await fetch(`${apiBase()}${path}`, {
    ...init,
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
  });
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new Error(`${res.status} ${res.statusText}${text ? `: ${text}` : ""}`);
  }
  if (res.status === 204) return undefined as T;
  return decode(await res.json());
}

export const api = {
  listSessions: (): Promise<SessionSummary[]> =>
    json("/api/sessions", decodeSessionSummaries),
  createSession: (body?: { cwd?: string; title?: string }) =>
    json<SessionDetail>("/api/sessions", decodeSessionDetail, {
      method: "POST",
      body: JSON.stringify(body ?? {}),
    }),
  getSession: (id: string) =>
    json<SessionDetail>(`/api/sessions/${id}`, decodeSessionDetail),
  patchSession: (id: string, body: { title?: string }) =>
    json<SessionDetail>(`/api/sessions/${id}`, decodeSessionDetail, {
      method: "PATCH",
      body: JSON.stringify(body),
    }),
  forkSession: (id: string, newId?: string) =>
    json<SessionDetail>(`/api/sessions/${id}/fork`, decodeSessionDetail, {
      method: "POST",
      body: JSON.stringify(newId ? { new_id: newId } : {}),
    }),
  deleteSession: (id: string) =>
    json<void>(`/api/sessions/${id}`, () => undefined, { method: "DELETE" }),
};
