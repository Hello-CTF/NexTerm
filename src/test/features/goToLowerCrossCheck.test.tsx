/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { goToLowerString } from "../../features/production/goFold";

// Expectations verified against Go 1.26.9 strings.ToLower.
const cases: [string, string][] = [
  ["ſ", "ſ"],
  ["s", "s"],
  ["S", "s"],
  ["ς", "ς"],
  ["σ", "σ"],
  ["Σ", "σ"],
  ["PRODUCTION", "production"],
  ["Production", "production"],
  ["İ", "i"],
  ["µ", "µ"],
  ["Ω", "ω"],
  ["ῳ", "ῳ"],
  ["ῼ", "ῳ"],
  ["ﬀ", "ﬀ"],
];

describe("goToLowerString cross-language consistency", () => {
  for (const [input, want] of cases) {
    it(`ToLower(${JSON.stringify(input)}) = ${JSON.stringify(want)}`, () => {
      expect(goToLowerString(input)).toBe(want);
    });
  }
});
