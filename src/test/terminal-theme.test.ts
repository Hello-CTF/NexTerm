import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");
const editorThemeSource = readFileSync(new URL("../ui/editorTheme.ts", import.meta.url), "utf8");

function luminance(hex: string): number {
  const n = parseInt(hex.slice(1), 16);
  const channels = [(n >> 16) & 255, (n >> 8) & 255, n & 255].map((v) => {
    const s = v / 255;
    return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  });
  return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
}

function contrast(fg: string, bg: string): number {
  const lf = luminance(fg);
  const lb = luminance(bg);
  const hi = Math.max(lf, lb);
  const lo = Math.min(lf, lb);
  return (hi + 0.05) / (lo + 0.05);
}

function blockBody(selector: string): string {
  const start = css.indexOf(selector);
  if (start < 0) throw new Error(`missing css block: ${selector}`);
  const open = css.indexOf("{", start);
  const close = css.indexOf("\n}", open);
  return css.slice(open + 1, close);
}

function vars(body: string, prefix: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const m of body.matchAll(new RegExp(`(${prefix}[\\w-]*):\\s*(#[0-9a-fA-F]{6})\\s*;`, "g"))) {
    out.set(m[1], m[2].toLowerCase());
  }
  return out;
}

const SLOTS = [
  "keyword",
  "string",
  "special",
  "number",
  "comment",
  "function",
  "type",
  "variable",
  "operator",
  "heading",
  "invalid",
  "fallback",
] as const;

const darkRoot = vars(blockBody(":root"), "--nx-cm-");
const lightTerm = vars(blockBody(':root[data-nx-term-theme="light"] .bg-term'), "--nx-cm-");
const lightTermVars = blockBody(':root[data-nx-term-theme="light"] .bg-term');

describe("editor highlight palettes", () => {
  it("defines every slot for the dark terminal palette", () => {
    for (const slot of SLOTS) {
      expect(darkRoot.get(`--nx-cm-${slot}`), `dark slot missing: ${slot}`).toBeTruthy();
    }
  });

  it("defines every slot for the light terminal palette", () => {
    for (const slot of SLOTS) {
      expect(lightTerm.get(`--nx-cm-${slot}`), `light slot missing: ${slot}`).toBeTruthy();
    }
  });

  it("editorTheme references the css slots instead of raw colors", () => {
    expect(editorThemeSource).not.toMatch(/#[0-9a-fA-F]{6}/);
    for (const slot of SLOTS) {
      expect(editorThemeSource).toContain(`var(--nx-cm-${slot})`);
    }
  });

  it("dark slots keep at least 4.5:1 on the dark terminal canvas", () => {
    for (const slot of SLOTS) {
      const value = darkRoot.get(`--nx-cm-${slot}`);
      if (!value) throw new Error(`missing dark slot ${slot}`);
      expect(contrast(value, "#101217"), `dark ${slot} ${value}`).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("light slots keep at least 4.5:1 on the light terminal canvas", () => {
    for (const slot of SLOTS) {
      const value = lightTerm.get(`--nx-cm-${slot}`);
      if (!value) throw new Error(`missing light slot ${slot}`);
      expect(contrast(value, "#f4f6fa"), `light ${slot} ${value}`).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("scopes the light terminal canvas and foreground to .bg-term", () => {
    expect(lightTermVars).toContain("--color-term: #f4f6fa");
    expect(lightTermVars).toContain("--nx-term-fg: #2f3642");
    expect(contrast("#2f3642", "#f4f6fa")).toBeGreaterThanOrEqual(4.5);
  });

  it("keeps the light term palette inside .bg-term so the interface stays untouched", () => {
    const selector = ':root[data-nx-term-theme="light"] .bg-term';
    expect(css.indexOf(selector)).toBeGreaterThan(css.indexOf(':root[data-nx-theme="light"] .bg-term'));
    const outside = css.replace(new RegExp(`\\n${selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*\\{[^}]*\\}`, "g"), "");
    expect(outside).not.toContain('--color-term: #f4f6fa');
  });
});
