/** @vitest-environment jsdom */

import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { nxHighlight } from "../ui/editorTheme";
import {
  customProperties,
  installCompiledCSS,
  requireStyleRule,
  styleRules,
} from "./lib/cssom";

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

let installed: HTMLStyleElement;
let rules: CSSStyleRule[];
let darkRoot: Map<string, string>;
let darkPalette: Map<string, string>;
let lightTerm: Map<string, string>;
let lightTermRule: CSSStyleRule;

beforeAll(async () => {
  installed = await installCompiledCSS("src/styles.css");
  rules = styleRules(installed);
  darkRoot = customProperties(requireStyleRule(rules, ":root", "--nx-cm-fallback"));
  darkPalette = customProperties(requireStyleRule(rules, ":root", "--color-term"));
  lightTermRule = requireStyleRule(
    rules,
    ':root[data-nx-term-theme="light"] .bg-term',
    "--nx-cm-fallback",
  );
  lightTerm = customProperties(lightTermRule);
});

afterAll(() => {
  installed?.remove();
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

function requiredColor(theme: Map<string, string>, slot: string): string {
  const value = theme.get(`nx-cm-${slot}`);
  if (!value) throw new Error(`missing editor color slot: ${slot}`);
  return value;
}

describe("editor highlight palettes", () => {
  it("defines every slot for dark and light terminal palettes", () => {
    for (const slot of SLOTS) {
      expect(darkRoot.get(`nx-cm-${slot}`), `dark slot missing: ${slot}`).toBeTruthy();
      expect(lightTerm.get(`nx-cm-${slot}`), `light slot missing: ${slot}`).toBeTruthy();
    }
  });

  it("mounts editor highlighting with theme variables for every slot", () => {
    const previousStyles = new Set(document.head.querySelectorAll("style"));
    const parent = document.createElement("div");
    document.body.append(parent);
    const view = new EditorView({
      parent,
      state: EditorState.create({ doc: "SELECT 1", extensions: [nxHighlight] }),
    });
    try {
      const editorStyles = [...document.head.querySelectorAll("style")].filter(
        (style) => !previousStyles.has(style),
      );
      const colors = editorStyles.flatMap((style) =>
        styleRules(style).flatMap((rule) => {
          const declarations = [];
          for (let index = 0; index < rule.style.length; index += 1) {
            if (rule.style.item(index) === "color") {
              declarations.push(rule.style.getPropertyValue("color").trim());
            }
          }
          return declarations;
        }),
      );
      expect(colors.length).toBeGreaterThan(0);
      for (const slot of SLOTS) {
        expect(colors, `editor does not use the ${slot} theme slot`).toContain(
          `var(--nx-cm-${slot})`,
        );
      }
    } finally {
      view.destroy();
      parent.remove();
      for (const style of document.head.querySelectorAll("style")) {
        if (!previousStyles.has(style)) style.remove();
      }
    }
  });

  it("keeps dark slots at least 4.5:1 on the dark terminal canvas", () => {
    const canvas = darkPalette.get("color-term");
    if (!canvas) throw new Error("missing dark terminal canvas");
    for (const slot of SLOTS) {
      const value = requiredColor(darkRoot, slot);
      expect(contrast(value, canvas), `dark ${slot} ${value}`).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("keeps light slots at least 4.5:1 on the light terminal canvas", () => {
    const canvas = lightTerm.get("color-term");
    if (!canvas) throw new Error("missing light terminal canvas");
    for (const slot of SLOTS) {
      const value = requiredColor(lightTerm, slot);
      expect(contrast(value, canvas), `light ${slot} ${value}`).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("scopes the light terminal canvas, foreground and palette to terminal surfaces", () => {
    const canvas = lightTermRule.style.getPropertyValue("--color-term").trim();
    const foreground = lightTermRule.style.getPropertyValue("--nx-term-fg").trim();
    expect(canvas).toBeTruthy();
    expect(foreground).toBeTruthy();
    expect(contrast(foreground, canvas)).toBeGreaterThanOrEqual(4.5);

    const lightCanvasRules = rules.filter(
      (rule) => rule.style.getPropertyValue("--color-term").trim() === canvas,
    );
    expect(lightCanvasRules.length).toBeGreaterThan(0);
    for (const rule of lightCanvasRules) {
      expect(rule.selectorText).toContain('[data-nx-term-theme="light"]');
      expect(rule.selectorText).toContain(".bg-term");
    }
  });
});

describe("terminal scrollbar", () => {
  it("keeps the xterm slider narrow and rounded inside its pointer area", () => {
    const slider = requireStyleRule(
      rules,
      ".xterm .xterm-scrollable-element > .scrollbar > .slider",
      "width",
    );
    expect(slider.style.getPropertyValue("left")).toBe("3px");
    expect(slider.style.getPropertyPriority("left")).toBe("important");
    expect(slider.style.getPropertyValue("width")).toBe("4px");
    expect(slider.style.getPropertyPriority("width")).toBe("important");
    expect(slider.style.getPropertyValue("border-radius")).toBe("8px");
  });
});
