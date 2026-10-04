import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");

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

function mix(fg: string, bg: string, alpha: number): string {
  const f = parseInt(fg.slice(1), 16);
  const b = parseInt(bg.slice(1), 16);
  const blended = [16, 8, 0].map((shift) =>
    Math.round(((f >> shift) & 255) * alpha + ((b >> shift) & 255) * (1 - alpha)),
  );
  return `#${blended.map((v) => v.toString(16).padStart(2, "0")).join("")}`;
}

function blockBody(selector: string, fromIndex = 0): string {
  const start = css.indexOf(selector, fromIndex);
  if (start < 0) throw new Error(`missing css block: ${selector}`);
  const open = css.indexOf("{", start);
  const close = css.indexOf("\n}", open);
  return css.slice(open + 1, close);
}

function colorVars(body: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const m of body.matchAll(/--(color-[\w-]+):\s*(#[0-9a-fA-F]{6})\s*;/g)) {
    out.set(m[1], m[2].toLowerCase());
  }
  return out;
}

const dark = colorVars(blockBody("@theme"));
const lightOverrides = colorVars(blockBody(':root[data-nx-theme="light"]'));
const light = new Map(dark);
for (const [key, value] of lightOverrides) light.set(key, value);

const themeIndependent = new Set(["color-accent", "color-green-100", "color-term"]);

describe("theme palettes", () => {
  it("defines a complete light palette", () => {
    for (const key of dark.keys()) {
      if (themeIndependent.has(key)) continue;
      expect(lightOverrides.has(key), `light palette misses ${key}`).toBe(true);
    }
  });

  it("keeps terminal and editor canvases dark in both themes", () => {
    for (const theme of [dark, light]) {
      expect(theme.get("color-term")).toBe("#101217");
    }
  });

  it("marks the early bootstrap with the resolved theme", () => {
    const html = readFileSync(new URL("../../index.html", import.meta.url), "utf8");
    const themeSource = readFileSync(new URL("../app/theme.ts", import.meta.url), "utf8");
    expect(html).toContain("nexterm.theme.v1");
    expect(html).toContain("dataset.nxTheme");
    expect(themeSource).toContain('"nexterm.theme.v1"');
  });
});

interface Pair {
  label: string;
  fg: string;
  bg: string;
}

function tintBg(theme: Map<string, string>, alpha: number): string {
  const get = (k: string) => {
    const v = theme.get(k);
    if (!v) throw new Error(`missing token ${k}`);
    return v;
  };
  return mix(get("color-accent"), get("color-neutral-900"), alpha);
}

function statePairs(theme: Map<string, string>): Pair[] {
  const get = (k: string) => {
    const v = theme.get(k);
    if (!v) throw new Error(`missing token ${k}`);
    return v;
  };
  const activeBg = tintBg(theme, 0.18);
  const focusBg = tintBg(theme, 0.24);
  const selectedBg = mix(get("color-accent"), get("color-neutral-950"), 0.16);
  const modelListBg = mix(get("color-neutral-950"), get("color-neutral-900"), 0.4);
  const modelBg = mix(get("color-blue-500"), modelListBg, 0.15);
  const onTint = get("color-neutral-300");
  return [
    { label: "command active hint on tint", fg: onTint, bg: activeBg },
    { label: "command active label on tint", fg: get("color-neutral-100"), bg: activeBg },
    { label: "menu hover hint on tint", fg: onTint, bg: activeBg },
    { label: "menu focus hint on tint", fg: onTint, bg: focusBg },
    { label: "menu focus label on tint", fg: get("color-neutral-100"), bg: focusBg },
    { label: "row selected meta on tint", fg: onTint, bg: selectedBg },
    { label: "row selected label on tint", fg: get("color-neutral-200"), bg: selectedBg },
    { label: "model current hint on tint", fg: onTint, bg: modelBg },
  ];
}

function textPairs(theme: Map<string, string>): Pair[] {
  const get = (k: string) => {
    const v = theme.get(k);
    if (!v) throw new Error(`missing token ${k}`);
    return v;
  };
  const pane = get("color-neutral-900");
  const canvas = get("color-neutral-950");
  const raised = get("color-neutral-800");
  const inputBg = mix(canvas, pane, 0.55);
  const pairs: Pair[] = [
    { label: "body on pane", fg: get("color-neutral-300"), bg: pane },
    { label: "body on canvas", fg: get("color-neutral-300"), bg: canvas },
    { label: "strong on raised", fg: get("color-neutral-100"), bg: raised },
    { label: "emphasis on raised", fg: get("color-neutral-200"), bg: raised },
    { label: "soft on pane", fg: get("color-neutral-400"), bg: pane },
    { label: "soft on canvas", fg: get("color-neutral-400"), bg: canvas },
    { label: "tertiary on pane", fg: get("color-neutral-500"), bg: pane },
    { label: "tertiary on canvas", fg: get("color-neutral-500"), bg: canvas },
    { label: "tertiary on raised", fg: get("color-neutral-500"), bg: raised },
    { label: "placeholder on input", fg: get("color-neutral-500"), bg: inputBg },
    { label: "red badge", fg: get("color-red-300"), bg: mix(get("color-red-500"), pane, 0.18) },
    { label: "green badge", fg: get("color-green-300"), bg: mix(get("color-green-500"), pane, 0.18) },
    { label: "amber badge", fg: get("color-amber-300"), bg: mix(get("color-amber-500"), pane, 0.18) },
    { label: "purple badge", fg: get("color-purple-300"), bg: mix(get("color-purple-500"), pane, 0.2) },
    { label: "blue accent on tint", fg: get("color-blue-300"), bg: mix(get("color-accent"), pane, 0.14) },
    { label: "primary button", fg: "#ffffff", bg: get("color-blue-600") },
    { label: "danger solid button", fg: "#ffffff", bg: get("color-red-600") },
  ];
  return pairs;
}

describe("text contrast", () => {
  const themes: [string, Map<string, string>][] = [
    ["dark", dark],
    ["light", light],
  ];

  for (const [name, theme] of themes) {
    for (const pair of textPairs(theme)) {
      it(`${name}: ${pair.label} >= 4.5:1`, () => {
        expect(contrast(pair.fg, pair.bg)).toBeGreaterThanOrEqual(4.5);
      });
    }
    for (const pair of statePairs(theme)) {
      it(`${name}: ${pair.label} >= 4.5:1`, () => {
        expect(contrast(pair.fg, pair.bg)).toBeGreaterThanOrEqual(4.5);
      });
    }
  }
});

describe("selected-state hint rules", () => {
  const a11y = readFileSync(new URL("../ui/a11y.css", import.meta.url), "utf8");

  it("defines the on-tint semantic foreground", () => {
    expect(css).toContain("--nx-fg-on-tint: var(--color-neutral-300)");
  });

  it("applies the on-tint foreground to active and selected hints", () => {
    expect(a11y).toContain(".nx-command-item.is-active .nx-command-hint");
    expect(a11y).toContain(".nx-menu-item:hover:not(:disabled) .nx-menu-hint");
    expect(a11y).toContain(".nx-menu-item:focus-visible .nx-menu-hint");
    expect(a11y).toContain(".nx-menu-hint.text-green-400\\/80");
    expect(a11y).toContain(".nx-row.is-selected .text-neutral-500");
    expect(a11y).toContain("color: var(--nx-fg-on-tint)");
  });
});

function semanticForeground(theme: Map<string, string>, rootBlock: string): string {
  const body = blockBody(rootBlock);
  const m = /--nx-fg-warning:\s*([^;]+);/.exec(body);
  if (!m) throw new Error(`missing --nx-fg-warning in ${rootBlock}`);
  const value = m[1].trim();
  const ref = /^var\((--[\w-]+)\)$/.exec(value);
  if (ref) {
    const resolved = theme.get(ref[1].replace(/^--/, ""));
    if (!resolved) throw new Error(`unresolved ${ref[1]} in ${rootBlock}`);
    return resolved;
  }
  if (/^#[0-9a-fA-F]{6}$/.test(value)) return value.toLowerCase();
  throw new Error(`unsupported --nx-fg-warning value: ${value}`);
}

describe("semantic warning foreground", () => {
  const themes: [string, Map<string, string>, string][] = [
    ["dark", dark, ":root"],
    ["light", light, ':root[data-nx-theme="light"]'],
  ];

  for (const [name, theme, block] of themes) {
    const fg = semanticForeground(theme, block);
    const canvas = theme.get("color-neutral-950");
    const accent = theme.get("color-accent");
    if (!canvas || !accent) throw new Error(`missing canvas/accent in ${name}`);
    const selectedTint = mix(accent, canvas, 0.16);

    it(`${name}: --nx-fg-warning resolves (${fg})`, () => {
      expect(fg).toMatch(/^#[0-9a-f]{6}$/);
    });
    it(`${name}: warning on canvas >= 4.5:1`, () => {
      expect(contrast(fg, canvas)).toBeGreaterThanOrEqual(4.5);
    });
    it(`${name}: warning on selected tint >= 4.5:1`, () => {
      expect(contrast(fg, selectedTint)).toBeGreaterThanOrEqual(4.5);
    });
  }

  it("keeps the two themes on distinct warning values", () => {
    expect(semanticForeground(dark, ":root")).not.toBe(
      semanticForeground(light, ':root[data-nx-theme="light"]'),
    );
  });
});
