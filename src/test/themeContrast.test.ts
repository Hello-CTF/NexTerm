/** @vitest-environment jsdom */

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import {
  customProperties,
  installCompiledCSS,
  requireStyleRule,
  styleRules,
} from "./lib/cssom";

let installed: HTMLStyleElement;
let a11yInstalled: HTMLStyleElement;
let rules: CSSStyleRule[];
let a11yRules: CSSStyleRule[];
let dark: Map<string, string>;
let light: Map<string, string>;
let lightOverrides: Map<string, string>;

function colorVars(rule: CSSStyleRule): Map<string, string> {
  const values = customProperties(rule);
  for (const key of values.keys()) {
    if (!key.startsWith("color-")) values.delete(key);
  }
  return values;
}

beforeAll(async () => {
  [installed, a11yInstalled] = await Promise.all([
    installCompiledCSS("src/styles.css"),
    installCompiledCSS("src/ui/a11y.css"),
  ]);
  rules = styleRules(installed);
  a11yRules = styleRules(a11yInstalled);
  dark = colorVars(requireStyleRule(rules, ":root", "--color-neutral-950"));
  lightOverrides = colorVars(
    requireStyleRule(rules, ':root[data-nx-theme="light"]', "--color-neutral-950"),
  );
  light = new Map(dark);
  for (const [key, value] of lightOverrides) light.set(key, value);
});

afterAll(() => {
  installed?.remove();
  a11yInstalled?.remove();
});

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
});

interface Pair {
  label: string;
  fg: string;
  bg: string;
}

function requiredColor(theme: Map<string, string>, key: string): string {
  const value = theme.get(key);
  if (!value) throw new Error(`missing token ${key}`);
  return value;
}

function tintBg(theme: Map<string, string>, alpha: number): string {
  return mix(
    requiredColor(theme, "color-accent"),
    requiredColor(theme, "color-neutral-900"),
    alpha,
  );
}

function statePairs(theme: Map<string, string>): Pair[] {
  const activeBg = tintBg(theme, 0.18);
  const focusBg = tintBg(theme, 0.24);
  const selectedBg = mix(
    requiredColor(theme, "color-accent"),
    requiredColor(theme, "color-neutral-950"),
    0.16,
  );
  const modelListBg = mix(
    requiredColor(theme, "color-neutral-950"),
    requiredColor(theme, "color-neutral-900"),
    0.4,
  );
  const modelBg = mix(requiredColor(theme, "color-blue-500"), modelListBg, 0.15);
  const onTint = requiredColor(theme, "color-neutral-300");
  return [
    { label: "command active hint on tint", fg: onTint, bg: activeBg },
    { label: "command active label on tint", fg: requiredColor(theme, "color-neutral-100"), bg: activeBg },
    { label: "menu hover hint on tint", fg: onTint, bg: activeBg },
    { label: "menu focus hint on tint", fg: onTint, bg: focusBg },
    { label: "menu focus label on tint", fg: requiredColor(theme, "color-neutral-100"), bg: focusBg },
    { label: "row selected meta on tint", fg: onTint, bg: selectedBg },
    { label: "row selected label on tint", fg: requiredColor(theme, "color-neutral-200"), bg: selectedBg },
    { label: "model current hint on tint", fg: onTint, bg: modelBg },
  ];
}

function textPairs(theme: Map<string, string>): Pair[] {
  const pane = requiredColor(theme, "color-neutral-900");
  const canvas = requiredColor(theme, "color-neutral-950");
  const raised = requiredColor(theme, "color-neutral-800");
  const inputBg = mix(canvas, pane, 0.55);
  return [
    { label: "body on pane", fg: requiredColor(theme, "color-neutral-300"), bg: pane },
    { label: "body on canvas", fg: requiredColor(theme, "color-neutral-300"), bg: canvas },
    { label: "strong on raised", fg: requiredColor(theme, "color-neutral-100"), bg: raised },
    { label: "emphasis on raised", fg: requiredColor(theme, "color-neutral-200"), bg: raised },
    { label: "soft on pane", fg: requiredColor(theme, "color-neutral-400"), bg: pane },
    { label: "soft on canvas", fg: requiredColor(theme, "color-neutral-400"), bg: canvas },
    { label: "tertiary on pane", fg: requiredColor(theme, "color-neutral-500"), bg: pane },
    { label: "tertiary on canvas", fg: requiredColor(theme, "color-neutral-500"), bg: canvas },
    { label: "tertiary on raised", fg: requiredColor(theme, "color-neutral-500"), bg: raised },
    { label: "placeholder on input", fg: requiredColor(theme, "color-neutral-500"), bg: inputBg },
    { label: "red badge", fg: requiredColor(theme, "color-red-300"), bg: mix(requiredColor(theme, "color-red-500"), pane, 0.18) },
    { label: "green badge", fg: requiredColor(theme, "color-green-300"), bg: mix(requiredColor(theme, "color-green-500"), pane, 0.18) },
    { label: "green status text on pane", fg: requiredColor(theme, "color-green-300"), bg: pane },
    { label: "amber badge", fg: requiredColor(theme, "color-amber-300"), bg: mix(requiredColor(theme, "color-amber-500"), pane, 0.18) },
    { label: "purple badge", fg: requiredColor(theme, "color-purple-300"), bg: mix(requiredColor(theme, "color-purple-500"), pane, 0.2) },
    { label: "blue accent on tint", fg: requiredColor(theme, "color-blue-300"), bg: mix(requiredColor(theme, "color-accent"), pane, 0.14) },
    { label: "primary button", fg: "#ffffff", bg: requiredColor(theme, "color-blue-600") },
    { label: "danger solid button", fg: "#ffffff", bg: requiredColor(theme, "color-red-600") },
  ];
}

describe("text contrast", () => {
  for (const name of ["dark", "light"] as const) {
    it(`${name}: text foregrounds reach 4.5:1 on their surfaces`, () => {
      const theme = name === "dark" ? dark : light;
      for (const pair of textPairs(theme)) {
        expect(contrast(pair.fg, pair.bg), pair.label).toBeGreaterThanOrEqual(4.5);
      }
    });

    it(`${name}: active and selected foregrounds reach 4.5:1 on tinted surfaces`, () => {
      const theme = name === "dark" ? dark : light;
      for (const pair of statePairs(theme)) {
        expect(contrast(pair.fg, pair.bg), pair.label).toBeGreaterThanOrEqual(4.5);
      }
    });
  }
});

describe("selected-state hint rules", () => {
  it("defines the on-tint semantic foreground", () => {
    const root = requireStyleRule(rules, ":root", "--nx-fg-on-tint");
    expect(root.style.getPropertyValue("--nx-fg-on-tint").trim()).toBe(
      "var(--color-neutral-300)",
    );
  });

  it("applies the on-tint foreground to active and selected hints", () => {
    const selectors = [
      ".nx-command-item.is-active .nx-command-hint",
      ".nx-menu-item:hover:not(:disabled) .nx-menu-hint",
      ".nx-menu-item:focus-visible .nx-menu-hint",
      ".nx-menu-hint.text-green-400\\/80",
      ".nx-row.is-selected .text-neutral-500",
    ];
    for (const selector of selectors) {
      const rule = requireStyleRule(a11yRules, selector, "color");
      expect(rule.style.getPropertyValue("color").trim(), selector).toBe(
        "var(--nx-fg-on-tint)",
      );
    }
  });
});

function semanticForeground(theme: Map<string, string>, selector: string): string {
  const value = requireStyleRule(rules, selector, "--nx-fg-warning").style
    .getPropertyValue("--nx-fg-warning")
    .trim();
  if (value.startsWith("var(") && value.endsWith(")")) {
    const reference = value.slice(4, -1).trim();
    const key = reference.startsWith("--") ? reference.slice(2) : reference;
    const resolved = theme.get(key);
    if (!resolved) throw new Error(`unresolved ${reference} in ${selector}`);
    return resolved;
  }
  if (value.startsWith("#") && value.length === 7) return value.toLowerCase();
  throw new Error(`unsupported --nx-fg-warning value: ${value}`);
}

describe("semantic warning foreground", () => {
  for (const name of ["dark", "light"] as const) {
    const selector = name === "dark" ? ":root" : ':root[data-nx-theme="light"]';

    it(`${name}: warning foreground resolves and remains readable`, () => {
      const theme = name === "dark" ? dark : light;
      const fg = semanticForeground(theme, selector);
      const canvas = requiredColor(theme, "color-neutral-950");
      const pane = requiredColor(theme, "color-neutral-900");
      const selectedTint = mix(requiredColor(theme, "color-accent"), canvas, 0.16);
      expect(fg.startsWith("#")).toBe(true);
      expect(fg).toHaveLength(7);
      expect(contrast(fg, canvas), "warning on canvas").toBeGreaterThanOrEqual(4.5);
      expect(contrast(fg, pane), "warning on pane").toBeGreaterThanOrEqual(4.5);
      expect(contrast(fg, selectedTint), "warning on selected tint").toBeGreaterThanOrEqual(4.5);
    });
  }

  it("keeps the two themes on distinct warning values", () => {
    expect(semanticForeground(dark, ":root")).not.toBe(
      semanticForeground(light, ':root[data-nx-theme="light"]'),
    );
  });
});

describe("light-theme status overrides", () => {
  it("uses the warning foreground for waiting and terminal warning indicators", () => {
    const selectors = [
      ':root[data-nx-theme="light"] .nx-ai-hitl-waiting',
      ':root[data-nx-theme="light"] .nx-ai-hitl-waiting svg',
      ':root[data-nx-theme="light"][data-nx-term-theme="light"] .nx-term-overlay-warn',
    ];
    for (const selector of selectors) {
      const rule = requireStyleRule(rules, selector, "color");
      expect(rule.style.getPropertyValue("color").trim(), selector).toBe("var(--nx-fg-warning)");
    }
  });

  it("darkens the workspace status dots", () => {
    const ok = requireStyleRule(rules, ':root[data-nx-theme="light"] .nx-ws-status.is-ok', "color");
    const warn = requireStyleRule(rules, ':root[data-nx-theme="light"] .nx-ws-status.is-warn', "color");
    expect(ok.style.getPropertyValue("color").trim()).toBe("var(--color-green-600)");
    expect(warn.style.getPropertyValue("color").trim()).toBe("var(--nx-fg-warning)");
  });

  it("keeps the waiting warning readable on the light pane", () => {
    const fg = semanticForeground(light, ':root[data-nx-theme="light"]');
    expect(contrast(fg, requiredColor(light, "color-neutral-900"))).toBeGreaterThanOrEqual(4.5);
  });
});
