import { describe, expect, it } from "vitest";
import { safeWebUrl } from "../../features/ai/urlSafety";

describe("Markdown URL policy", () => {
  it("accepts absolute HTTP and HTTPS URLs regardless of scheme case", () => {
    expect(safeWebUrl("https://example.test/a?q=1")).toBe("https://example.test/a?q=1");
    expect(safeWebUrl("HTTP://example.test/")).toBe("http://example.test/");
  });

  for (const raw of [
    "javascript:alert(1)",
    "JaVaScRiPt:alert(1)",
    "data:text/html,hello",
    "vbscript:msgbox(1)",
    "file:///etc/passwd",
    "ssh://example.test",
    "//example.test/path",
    "/relative/path",
    "not a url",
  ]) {
    it(`rejects ${raw}`, () => {
      expect(safeWebUrl(raw)).toBeNull();
    });
  }
});
