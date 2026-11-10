/** @vitest-environment jsdom */

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { ACCENT_PRESETS, ACCENT_SCALE_STEPS, accentVariables, DEFAULT_ACCENT } from "../app/accentPresets";
import { contrastRatio, mixHex } from "../features/terminal/terminalSchemes";
import { customProperties, installCompiledCSS, requireStyleRule, styleRules } from "./lib/cssom";

let installed: HTMLStyleElement;
let rules: CSSStyleRule[];
let dark: Map<string, string>;
let light: Map<string, string>;

beforeAll(async () => {
  installed = await installCompiledCSS("src/styles.css");
  rules = styleRules(installed);
  dark = customProperties(requireStyleRule(rules, ":root", "--color-neutral-950"));
  light = new Map(dark);
  for (const [key, value] of customProperties(requireStyleRule(rules, ':root[data-nx-theme="light"]', "--color-neutral-950"))) {
    light.set(key, value);
  }
});

afterAll(() => {
  installed?.remove();
});

function token(theme: Map<string, string>, key: string): string {
  const value = theme.get(key);
  if (!value) throw new Error(`missing token ${key}`);
  return value;
}

describe("accent presets", () => {
  it("keeps the shipped tokens as the default preset", () => {
    const indigo = ACCENT_PRESETS.find((preset) => preset.id === DEFAULT_ACCENT)!;
    // The test build has no utility candidates, so Tailwind leaves out theme tokens nothing references.
    const compiled = ACCENT_SCALE_STEPS.filter((step) => dark.has(step === "accent" ? "color-accent" : `color-blue-${step}`));
    expect(compiled).toEqual(expect.arrayContaining(["accent", "300", "500", "600"]));
    for (const step of compiled) {
      const key = step === "accent" ? "color-accent" : `color-blue-${step}`;
      expect(indigo.dark[step], `dark ${key}`).toBe(token(dark, key));
      expect(indigo.light[step], `light ${key}`).toBe(token(light, key));
    }
    expect(accentVariables(DEFAULT_ACCENT)).toEqual({});
  });

  it("keeps accent text, tints and solid buttons readable in both themes", () => {
    for (const preset of ACCENT_PRESETS) {
      for (const [name, theme] of [["dark", dark], ["light", light]] as const) {
        const scale = preset[name];
        const pane = token(theme, "color-neutral-900");
        const canvas = token(theme, "color-neutral-950");
        const pairs = [
          { label: "accent text on tint", fg: scale["300"], bg: mixHex(scale.accent, pane, 0.14) },
          { label: "primary button", fg: "#ffffff", bg: scale["600"] },
          { label: "hint on active tint", fg: token(theme, "color-neutral-300"), bg: mixHex(scale.accent, pane, 0.24) },
          { label: "label on active tint", fg: token(theme, "color-neutral-100"), bg: mixHex(scale.accent, pane, 0.24) },
          { label: "label on selected row", fg: token(theme, "color-neutral-200"), bg: mixHex(scale.accent, canvas, 0.16) },
        ];
        for (const pair of pairs) {
          expect(contrastRatio(pair.fg, pair.bg), `${preset.id} ${name} ${pair.label}`).toBeGreaterThanOrEqual(4.5);
        }
      }
    }
  });

  it("never offers the colors that mark production hosts, errors or warnings", () => {
    const reserved = ["color-red-500", "color-red-600", "color-amber-400", "color-amber-500"].map((key) => token(dark, key));
    for (const preset of ACCENT_PRESETS) {
      expect(reserved).not.toContain(preset.dark.accent);
      expect(preset.id).not.toMatch(/red|rose|amber|orange/);
    }
  });

  it("maps the root variables onto the accent and blue tokens in every theme scope", () => {
    const scopes = [
      { selector: ":root[data-nx-accent]", theme: "dark" },
      { selector: ':root[data-nx-accent][data-nx-theme="light"]', theme: "light" },
      { selector: ':root[data-nx-accent][data-nx-theme="light"] .bg-term', theme: "dark" },
      { selector: ':root[data-nx-accent][data-nx-term-theme="light"] .bg-term', theme: "light" },
    ];
    for (const { selector, theme } of scopes) {
      const values = customProperties(requireStyleRule(rules, selector, "--color-accent"));
      for (const step of ACCENT_SCALE_STEPS) {
        const key = step === "accent" ? "color-accent" : `color-blue-${step}`;
        expect(values.get(key), `${selector} ${key}`).toBe(`var(--nx-accent-${theme}-${step})`);
      }
    }
    const pane = customProperties(requireStyleRule(rules, ":root[data-nx-term-scheme] .nx-terminal-pane.bg-term", "--color-term"));
    expect(pane.get("color-term")).toBe("var(--nx-term-scheme-bg)");
  });

  it("leaves the production pane outline on the red token", () => {
    const outline = requireStyleRule(rules, ".nx-pane-production", "outline").style.getPropertyValue("outline");
    expect(outline).toContain("var(--color-red-500)");
  });
});
