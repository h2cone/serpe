import { describe, expect, it } from "vitest";
import { shortPath, titleFromPrompt } from "./prefs";

describe("shortPath", () => {
  it("keeps short paths intact", () => {
    expect(shortPath("/work")).toBe("/work");
  });

  it("keeps the last two segments of a long path", () => {
    expect(shortPath("C:\\Users\\tw8ap\\projc\\serpe")).toBe("projc/serpe");
  });
});

describe("titleFromPrompt", () => {
  it("uses the first line and trims", () => {
    expect(titleFromPrompt("  Hello\nworld")).toBe("Hello");
  });
});
