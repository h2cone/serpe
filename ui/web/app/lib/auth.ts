/**
 * The API credential deliberately lives only in this JavaScript realm. It is
 * never serialized, put in a URL, copied to Web Storage, or sent as a cookie.
 */

const tokenPattern = /^[A-Za-z0-9\-._~+/]+={0,}$/;

export class AuthRequiredError extends Error {
  constructor() {
    super("API access token required");
    this.name = "AuthRequiredError";
  }
}

export type AuthSnapshot = Readonly<{
  token: string | null;
  issue: string | null;
}>;

const anonymousSnapshot: AuthSnapshot = Object.freeze({
  token: null,
  issue: null,
});

let snapshot = anonymousSnapshot;
const listeners = new Set<() => void>();

export function validateAPIToken(value: string): string | null {
  if (value.length < 32 || value.length > 4096) {
    return "Token must contain 32–4096 ASCII characters.";
  }
  if (!tokenPattern.test(value)) {
    return "Token is not in the server's bearer-token format.";
  }
  return null;
}

export function authSnapshot(): AuthSnapshot {
  return snapshot;
}

export function serverAuthSnapshot(): AuthSnapshot {
  return anonymousSnapshot;
}

export function subscribeAuth(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function publish(next: AuthSnapshot) {
  snapshot = Object.freeze(next);
  for (const listener of listeners) listener();
}

export function setAPIToken(value: string): void {
  const problem = validateAPIToken(value);
  if (problem) throw new Error(problem);
  if (typeof window === "undefined") {
    throw new Error("API tokens may only be set in a browser tab");
  }
  publish({ token: value, issue: null });
}

export function clearAPIToken(): void {
  publish(anonymousSnapshot);
}

/** Clear a rejected secret without retaining or echoing any part of it. */
export function rejectAPIToken(): void {
  publish({
    token: null,
    issue: "The server rejected that token. Check the token file and try again.",
  });
}

export function authorizationHeader(): string {
  if (!snapshot.token) throw new AuthRequiredError();
  return `Bearer ${snapshot.token}`;
}

export function isAuthRequired(error: unknown): error is AuthRequiredError {
  return error instanceof AuthRequiredError;
}
