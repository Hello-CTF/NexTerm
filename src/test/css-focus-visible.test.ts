/** @vitest-environment jsdom */

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { declared, installCompiledCSS, styleRules } from "./lib/cssom";

let installed: HTMLStyleElement[];
let rules: CSSStyleRule[];
let a11yRules: CSSStyleRule[];

beforeAll(async () => {
  const [app, a11y] = await Promise.all([
    installCompiledCSS("src/styles.css"),
    installCompiledCSS("src/ui/a11y.css"),
  ]);
  installed = [app, a11y];
  rules = styleRules(app);
  a11yRules = styleRules(a11y);
});

afterAll(() => installed?.forEach((element) => element.remove()));

// Tailwind scans test sources for utility candidates; spelling this shorthand property name as a
// single word would emit an unused utility into the production bundle.
const OUTLINE = ["out", "line"].join("");

describe("focus-visible contract", () => {
  it("does not draw a focus ring for the auto-focused command palette input", () => {
    expect(declared(a11yRules, ".nx-overlay .nx-command-input:focus-visible", OUTLINE)).toMatch(
      /^none/,
    );
  });

  it("draws a focus ring when a CodeMirror editor is focused", () => {
    const selector = ".cm-editor.cm-focused:focus-within";
    expect(declared(rules, selector, OUTLINE)).toContain("2px solid var(--nx-focus-ring)");
    expect(declared(rules, selector, "outline-offset")).toContain("-2px");
  });

  it("draws a focus ring when a CodeMirror panel input is focused", () => {
    expect(declared(rules, ".cm-editor .cm-textfield:focus-visible", OUTLINE)).toContain(
      "2px solid var(--nx-focus-ring)",
    );
  });

  it("maps overlay focus rings to Highlight in forced-colors mode", () => {
    const color = declared(
      a11yRules,
      ".nx-overlay :focus-visible",
      "outline-color",
      "(forced-colors: active)",
    );
    expect(color?.toLowerCase()).toBe("highlight");
  });
});
