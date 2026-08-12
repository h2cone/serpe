import { describe, expect, it } from "vitest";
import { apiOrigin, DEFAULT_API_ORIGIN } from "../../api-origin";

describe("apiOrigin", () => {
  it("uses one default and trims an explicit override", () => {
    expect(apiOrigin({})).toBe(DEFAULT_API_ORIGIN);
    expect(apiOrigin({ SERPE_API_ORIGIN: "  http://api.internal:9000  " })).toBe(
      "http://api.internal:9000",
    );
    expect(apiOrigin({ SERPE_API_ORIGIN: "  " })).toBe(DEFAULT_API_ORIGIN);
  });
});
