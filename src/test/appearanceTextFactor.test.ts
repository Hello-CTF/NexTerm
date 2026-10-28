/** @vitest-environment jsdom */

import { readFileSync } from "node:fs";
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

// Compiled CSS may drop whitespace around operators.
function compact(value: string | undefined): string {
  return (value ?? "").replace(/\s+/g, "");
}

function fontSizes(source: CSSStyleRule[]): string[] {
  return source
    .map((rule) => rule.style.getPropertyValue("font-size").trim())
    .filter((value) => value.length > 0);
}

const html = readFileSync("index.html", "utf8");

const sources = import.meta.glob("../{app,features,ipc,ui}/**/*.{ts,tsx}", {
  eager: true,
  query: "?raw",
  import: "default",
}) as Record<string, string>;

function tailwindPxValues(): Set<string> {
  const values = new Set<string>();
  for (const text of Object.values(sources)) {
    for (const m of text.matchAll(/text-\[([0-9.]+)px\]/g)) values.add(m[1]);
  }
  return values;
}

describe("unified text factor wiring", () => {
  it("drives every styles.css font size from the unified factor", () => {
    const offenders = fontSizes(rules).filter((size) => size.includes("--nx-ui-scale"));
    expect(offenders).toEqual([]);
    expect(fontSizes(rules)).toContain("calc(13px * var(--nx-ui-text-factor))");
    expect(declared(rules, ":root", "--nx-ui-text-factor")).toBe("1");
  });

  it("overrides every Tailwind text-[Npx] utility used in src with the factor", () => {
    const values = tailwindPxValues();
    expect(values.size).toBeGreaterThan(0);
    for (const value of values) {
      const selector = `.text-\\[${value.replace(/\./g, "\\.")}px\\]`;
      expect(
        compact(declared(rules, selector, "font-size")),
        `override for text-[${value}px] must exist and use the factor`,
      ).toBe(compact(`calc(${value}px * var(--nx-ui-text-factor))`));
    }
  });

  it("scales the Tailwind text-xs theme token with the factor", () => {
    expect(compact(declared(rules, ":root", "--text-xs"))).toBe(
      compact("calc(0.75rem * var(--nx-ui-text-factor))"),
    );
  });

  it("drives every a11y.css font size from the unified factor", () => {
    const sizes = fontSizes(a11yRules);
    expect(sizes.length).toBeGreaterThan(0);
    for (const size of sizes) {
      expect(size, `a11y.css font size misses the factor: ${size}`).toContain(
        "var(--nx-ui-text-factor)",
      );
    }
    expect(declared(a11yRules, ".nx-prompt-hint", "font-size")).toBe(
      "calc(11px * var(--nx-ui-text-factor))",
    );
    expect(declared(a11yRules, ".nx-command-error", "font-size")).toBe(
      "calc(12px * var(--nx-ui-text-factor))",
    );
  });

  it("bootstraps appearance before first paint in index.html", () => {
    expect(html).toContain("nexterm.appearance.v1");
    expect(html).toContain("--nx-ui-text-factor");
    expect(html).toContain("--nx-ui-font-size");
    expect(html).toContain("nxTermTheme");
    expect(html.indexOf("nexterm.appearance.v1")).toBeLessThan(html.indexOf('src="/src/main.tsx"'));
  });
});
