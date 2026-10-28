import { describe, expect, it } from "vitest";
import { normalizeLeftMode } from "../../app/layoutFocus";

describe("normalizeLeftMode", () => {
  it.each(["assets", "credentials", "files"] as const)("keeps %s", (mode) => {
    expect(normalizeLeftMode(mode)).toBe(mode);
  });

  it.each([undefined, null, "", "other", 3])("falls back to assets for %j", (raw) => {
    expect(normalizeLeftMode(raw)).toBe("assets");
  });
});
