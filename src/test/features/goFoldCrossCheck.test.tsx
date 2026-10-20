/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { goEqualFold, goTrimSpace } from "../../features/production/goFold";

// Expectations verified against Go 1.26.9 strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)).
const cases: [string, string, boolean][] = [
  ["ſ", "s", true],
  ["ſ", "S", true],
  ["ς", "σ", true],
  ["ς", "Σ", true],
  ["Σ", "σ", true],
  ["İ", "i", false],
  ["İ", "i\u0307", false],
  ["PRODUCTION", "production", true],
  [" Production ", "production", true],
  ["\u0085production\u0085", "production", true],
  ["\uFEFFproduction", "production", false],
  ["µ", "μ", true],
  ["µ", "Μ", true],
  ["å", "Å", true],
  ["å", "Å", true],
  ["ß", "ẞ", true],
  ["ß", "ss", false],
  ["k", "K", true],
  ["k", "K", true],
  ["K", "K", true],
  ["ῳ", "ῼ", true],
  ["ᾀ", "ᾈ", true],
  ["ﬀ", "ff", false],
  ["ﬁ", "fi", false],
  ["Ǆ", "ǆ", true],
  ["ǅ", "Ǆ", true],
];

describe("goEqualFold cross-language consistency", () => {
  for (const [a, b, want] of cases) {
    it(`${JSON.stringify(a)} == ${JSON.stringify(b)} -> ${want}`, () => {
      expect(goEqualFold(goTrimSpace(a), goTrimSpace(b))).toBe(want);
    });
  }
});
