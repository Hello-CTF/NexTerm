/** @vitest-environment jsdom */

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { installCompiledCSS, requireStyleRule, styleRules } from "./lib/cssom";

let installed: HTMLStyleElement;
let sheetInstalled: HTMLStyleElement;
let rules: CSSStyleRule[];
let sheetRules: CSSStyleRule[];

beforeAll(async () => {
  [installed, sheetInstalled] = await Promise.all([
    installCompiledCSS("src/styles.css"),
    installCompiledCSS("src/ui/contextMenuSheet.css"),
  ]);
  rules = styleRules(installed);
  sheetRules = styleRules(sheetInstalled);
});

afterAll(() => {
  installed?.remove();
  sheetInstalled?.remove();
});

function exactRules(selector: string): CSSStyleRule[] {
  return rules.filter((rule) =>
    rule.selectorText.split(",").some((part) => part.trim() === selector),
  );
}

describe("app safe-area shell", () => {
  it("reserves the bottom inset on the whole app instead of only the status bar", () => {
    const app = requireStyleRule(rules, ".nx-app", "padding-bottom");
    expect(app.style.getPropertyValue("padding-bottom").trim()).toBe("var(--nx-safe-bottom)");

    const statusBars = exactRules(".nx-statusbar");
    expect(statusBars.length).toBeGreaterThan(0);
    for (const statusBar of statusBars) {
      expect(statusBar.style.getPropertyValue("padding-bottom")).not.toContain("--nx-safe-bottom");
    }

    const safeBottom = requireStyleRule(rules, ":root", "--nx-safe-bottom");
    const value = safeBottom.style.getPropertyValue("--nx-safe-bottom");
    expect(value).toContain("--nx-inset-bottom");
    expect(value).toContain("env(safe-area-inset-bottom");
  });

  it("paints the exposed inset with the theme canvas in dark and light modes", () => {
    const body = requireStyleRule(rules, "body", "background");
    expect(body.style.getPropertyValue("background").trim()).toBe("var(--nx-bg-canvas)");

    const root = requireStyleRule(rules, ":root", "--nx-bg-canvas");
    expect(root.style.getPropertyValue("--nx-bg-canvas").trim()).toBe("var(--color-neutral-950)");
    const darkCanvas = requireStyleRule(rules, ":root", "--color-neutral-950")
      .style.getPropertyValue("--color-neutral-950")
      .trim();
    const lightCanvas = requireStyleRule(
      rules,
      ':root[data-nx-theme="light"]',
      "--color-neutral-950",
    ).style.getPropertyValue("--color-neutral-950").trim();
    expect(lightCanvas).not.toBe(darkCanvas);
  });

  it("keeps keyboard displacement separate from the safe-area reservation", () => {
    const app = requireStyleRule(rules, ".nx-app", "padding-bottom");
    expect(app.style.getPropertyValue("padding-bottom")).not.toContain("--nx-kb-inset");

    for (const selector of [".nx-statusbar", ".nx-terminal-keys"]) {
      const rule = requireStyleRule(rules, selector, "transform");
      expect(rule.style.getPropertyValue("transform")).toContain("--nx-kb-inset");
    }
  });
});

describe("context menu sheet safe area", () => {
  it("extends its themed background while keeping menu content above the inset", () => {
    const sheet = requireStyleRule(sheetRules, ".nx-menu-sheet", "padding-bottom");
    expect(sheet.style.getPropertyValue("padding-bottom").trim()).toBe("var(--nx-safe-bottom)");
    expect(sheet.style.getPropertyValue("background").trim()).toBe("var(--nx-bg-pane)");

    const list = requireStyleRule(sheetRules, ".nx-menu-sheet-list", "overflow-y");
    expect(list.style.getPropertyValue("overflow-y").trim()).toBe("auto");
  });
});
