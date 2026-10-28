import { describe, expect, it } from "vitest";
import { normalizeLeftMode } from "../../app/layoutFocus";

describe("normalizeLeftMode", () => {
  it("keeps credentials and assets", () => {
    expect(normalizeLeftMode("credentials")).toBe("credentials");
    expect(normalizeLeftMode("assets")).toBe("assets");
  });

  it.each(["files", undefined, null, "", "other", 3])("falls back to assets for %j", (raw) => {
    expect(normalizeLeftMode(raw)).toBe("assets");
  });
});
