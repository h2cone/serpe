import type { SessionDetail, SessionSummary } from "./types";

/** Browser: same origin. SSR/dev: Go API (Vite proxies in browser only). */
function apiBase(): string {
  if (typeof window !== "undefined") return "";
  return process.env.OURO_GO_ORIGIN ?? "http://127.0.0.1:8080";
}

async function json<T>(path: string, init?: RequestInit): Promise<T> {
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
  return res.json() as Promise<T>;
}

export const api = {
  listSessions: () => json<SessionSummary[]>("/api/sessions"),
  createSession: (body?: { cwd?: string; title?: string }) =>
    json<SessionDetail>("/api/sessions", {
      method: "POST",
      body: JSON.stringify(body ?? {}),
    }),
  getSession: (id: string) => json<SessionDetail>(`/api/sessions/${id}`),
  patchSession: (id: string, body: { title?: string }) =>
    json<SessionDetail>(`/api/sessions/${id}`, {
      method: "PATCH",
      body: JSON.stringify(body),
    }),
  forkSession: (id: string, newId?: string) =>
    json<SessionDetail>(`/api/sessions/${id}/fork`, {
      method: "POST",
      body: JSON.stringify(newId ? { new_id: newId } : {}),
    }),
  deleteSession: (id: string) =>
    json<void>(`/api/sessions/${id}`, { method: "DELETE" }),
};
