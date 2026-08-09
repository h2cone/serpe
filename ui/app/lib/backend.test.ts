import { describe, expect, it } from "vitest";
import { DEFAULT_GO_ORIGIN, goOrigin } from "../../backend";

describe("goOrigin", () => {
  it("uses one default and trims an explicit override", () => {
    expect(goOrigin({})).toBe(DEFAULT_GO_ORIGIN);
    expect(goOrigin({ SERPE_GO_ORIGIN: "  http://api.internal:9000  " })).toBe(
      "http://api.internal:9000",
    );
    expect(goOrigin({ SERPE_GO_ORIGIN: "  " })).toBe(DEFAULT_GO_ORIGIN);
  });
});
