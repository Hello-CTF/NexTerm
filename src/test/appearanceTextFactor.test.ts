import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");
const a11y = readFileSync(new URL("../ui/a11y.css", import.meta.url), "utf8");
const html = readFileSync(new URL("../../index.html", import.meta.url), "utf8");

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
    const offenders = [...css.matchAll(/font-size:[^;}]*--nx-ui-scale[^;}]*;/g)];
    expect(offenders.map((m) => m[0])).toEqual([]);
    expect(css).toContain("font-size: calc(13px * var(--nx-ui-text-factor));");
    expect(css).toContain("--nx-ui-text-factor: 1;");
  });

  it("overrides every Tailwind text-[Npx] utility used in src with the factor", () => {
    const values = tailwindPxValues();
    expect(values.size).toBeGreaterThan(0);
    for (const value of values) {
      const escaped = value.replace(/\./g, "\\.");
      expect(css, `missing factor override for text-[${value}px]`).toContain(
        `.text-\\[${escaped}px\\]`,
      );
      expect(css, `override for text-[${value}px] must use the factor`).toContain(
        `font-size: calc(${value}px * var(--nx-ui-text-factor));`,
      );
    }
  });

  it("scales the Tailwind text-xs theme token with the factor", () => {
    expect(css).toContain("--text-xs: calc(0.75rem * var(--nx-ui-text-factor));");
  });

  it("drives every a11y.css font size from the unified factor", () => {
    const sizes = [...a11y.matchAll(/font-size:[^;}]*;/g)].map((m) => m[0]);
    expect(sizes.length).toBeGreaterThan(0);
    for (const size of sizes) {
      expect(size, `a11y.css font size misses the factor: ${size}`).toContain(
        "var(--nx-ui-text-factor)",
      );
    }
    expect(a11y).toMatch(
      /\.nx-prompt-hint\s*\{[^}]*font-size:\s*calc\(11px \* var\(--nx-ui-text-factor\)\)/,
    );
    expect(a11y).toMatch(
      /\.nx-command-error\s*\{[^}]*font-size:\s*calc\(12px \* var\(--nx-ui-text-factor\)\)/,
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
