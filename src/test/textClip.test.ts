import { afterEach, describe, expect, it } from "vitest";
import { clipText, graphemes, setGraphemeSegmenterForTest } from "../ui/textClip";

const FAMILY = "👨\u200d👩\u200d👧\u200d👦";
const FLAG_JP = "🇯🇵";
const FLAG_US = "🇺🇸";
const THUMBS = "👍🏽";
const KEYCAP = "1\ufe0f\u20e3";
const HEART = "❤\ufe0f";
const SCOTLAND = "🏴\u{e0067}\u{e0062}\u{e0073}\u{e0063}\u{e0074}\u{e007f}";
const ACCENTED = "e\u0301";
const CLUSTERS = [FAMILY, FLAG_JP, THUMBS, KEYCAP, HEART, SCOTLAND, ACCENTED, "中"];

const BROKEN = /[\ud800-\udbff](?![\udc00-\udfff])|(?<![\ud800-\udbff])[\udc00-\udfff]|�/;
const codePoints = (s: string) => Array.from(s).length;

for (const mode of ["Intl.Segmenter", "fallback"] as const) {
  describe(`clipText (${mode})`, () => {
    afterEach(() => setGraphemeSegmenterForTest(undefined));
    const useMode = () => setGraphemeSegmenterForTest(mode === "fallback" ? null : undefined);

    it("segments emoji sequences as single clusters", () => {
      useMode();
      expect(graphemes(CLUSTERS.join(""))).toEqual(CLUSTERS);
      expect(graphemes(FLAG_JP + FLAG_US + "🇯")).toEqual([FLAG_JP, FLAG_US, "🇯"]);
    });

    it("drops a cluster straddling the limit whole", () => {
      useMode();
      for (const cluster of CLUSTERS) {
        const prefix = "中文";
        const text = `${prefix}${cluster}tail`;
        const size = codePoints(cluster);
        for (let limit = 2; limit < 2 + size; limit++) expect(clipText(text, limit)).toBe(prefix);
        expect(clipText(text, 2 + size)).toBe(prefix + cluster);
      }
    });

    it("returns short text unchanged and honours the code point cap", () => {
      useMode();
      expect(clipText(FAMILY, 80)).toBe(FAMILY);
      expect(clipText("abc", 0)).toBe("");
      const text = CLUSTERS.join("").repeat(6);
      for (let limit = 0; limit <= codePoints(text); limit++) {
        const out = clipText(text, limit);
        expect(codePoints(out)).toBeLessThanOrEqual(limit);
        expect(text.startsWith(out)).toBe(true);
        expect(out).not.toMatch(BROKEN);
        expect(out.endsWith("\u200d")).toBe(false);
      }
    });
  });
}
