/** @vitest-environment jsdom */

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { flattenCSSRules, installCompiledCSS, requireStyleRule, styleRules } from "./lib/cssom";
import { TERMINAL_FONT_STACKS } from "../features/terminal/xtermTheme";

let installed: HTMLStyleElement;

beforeAll(async () => {
  installed = await installCompiledCSS("src/styles.css");
});

afterAll(() => installed?.remove());

function firstFamily(stack: string): string {
  return stack.split(",")[0].trim().replace(/^['"]|['"]$/g, "");
}

// jsdom's CSSOM drops src and unicode-range descriptors, so the compiled stylesheet text
// (what the app ships) is read for them; the rule itself must still parse as @font-face.
function emojiFontFace(): string {
  const parsed = flattenCSSRules(installed.sheet!.cssRules).some(
    (rule) => rule.cssText.startsWith("@font-face") && rule.cssText.includes("NexTerm Emoji"),
  );
  if (!parsed) throw new Error("missing NexTerm Emoji @font-face");
  const block = (installed.textContent ?? "")
    .split("@font-face")
    .slice(1)
    .map((chunk) => chunk.slice(0, chunk.indexOf("}")))
    .find((chunk) => chunk.includes("NexTerm Emoji"));
  if (!block) throw new Error("missing NexTerm Emoji @font-face text");
  return block;
}

function unicodeRanges(): Array<[number, number]> {
  const value = /unicode-range:\s*([^;}]+)/.exec(emojiFontFace())?.[1] ?? "";
  return value.split(",").map((part) => {
    const [start, end] = part.trim().replace(/^U\+/i, "").split("-");
    return [parseInt(start, 16), parseInt(end ?? start, 16)];
  });
}

const covers = (cp: number) => unicodeRanges().some(([start, end]) => cp >= start && cp <= end);

describe("colour emoji font alias", () => {
  it("leads the UI font stacks", () => {
    const rules = styleRules(installed);
    for (const token of ["--font-sans", "--font-mono"]) {
      const value = requireStyleRule(rules, ":root", token).style.getPropertyValue(token);
      expect(firstFamily(value), token).toBe("NexTerm Emoji");
      expect(value, token).toContain("Noto Color Emoji");
    }
  });

  it("stays out of the terminal stacks, which keep emoji families only as trailing fallbacks", () => {
    // Terminal programs lay out text-default symbols as one-cell glyphs of the primary font.
    for (const [family, stack] of Object.entries(TERMINAL_FONT_STACKS)) {
      expect(stack, family).not.toContain("NexTerm Emoji");
      expect(stack.indexOf("monospace"), family).toBeLessThan(stack.indexOf("Noto Color Emoji"));
    }
  });

  it("maps local colour emoji fonts", () => {
    const src = emojiFontFace();
    for (const name of ["Apple Color Emoji", "Segoe UI Emoji", "Noto Color Emoji"]) {
      expect(src).toContain(`local("${name}")`);
    }
  });

  it("covers emoji code points only, leaving text, keycap bases and TUI glyphs to the primary font", () => {
    for (const cp of [0x2764, 0x26a0, 0x2600, 0x2b50, 0xfe0f, 0x200d, 0x1f600, 0x1f1ef, 0x1f3fd, 0xe0067]) {
      expect(covers(cp), cp.toString(16)).toBe(true);
    }
    for (let cp = 0; cp <= 0x7f; cp++) expect(covers(cp), cp.toString(16)).toBe(false);
    // Plain arrows, keycap mark, box drawing and block elements used by TUIs.
    for (const cp of [0x2190, 0x2191, 0x2192, 0x2193, 0x20e3, 0x2500, 0x2502, 0x253c, 0x2588, 0x2591]) {
      expect(covers(cp), cp.toString(16)).toBe(false);
    }
  });
});
