import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import {
  authorizationHeader,
  clearAPIToken,
  setAPIToken,
  validateAPIToken,
} from "./auth";

afterEach(() => {
  clearAPIToken();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("tab-memory API authentication", () => {
  it("validates the server bearer grammar", () => {
    expect(validateAPIToken("a".repeat(31))).not.toBeNull();
    expect(validateAPIToken("a".repeat(32))).toBeNull();
    expect(validateAPIToken(`${"a".repeat(31)}=`)).toBeNull();
    expect(validateAPIToken(`${"a".repeat(31)} `)).not.toBeNull();
    expect(validateAPIToken(`${"a".repeat(31)}?`)).not.toBeNull();
  });

  it("puts the token only in the Authorization header", async () => {
    vi.stubGlobal("window", {});
    const token = "A".repeat(32);
    setAPIToken(token);
    const fetchMock = vi.fn(async () =>
      new Response("[]", {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await api.listSessions();

    const [url, init] = fetchMock.mock.calls[0] as unknown as [
      string,
      RequestInit,
    ];
    expect(url).toBe("/api/sessions");
    expect(url).not.toContain(token);
    expect(new Headers(init.headers).get("Authorization")).toBe(
      `Bearer ${token}`,
    );
    expect(init.credentials).toBe("omit");
    expect(init.cache).toBe("no-store");
    expect(authorizationHeader()).toBe(`Bearer ${token}`);
  });
});
