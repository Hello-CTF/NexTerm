/** @vitest-environment jsdom */

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import {
  flattenCSSRules,
  installCompiledCSS,
  requireStyleRule,
  styleRules,
} from "./lib/cssom";

let installed: HTMLStyleElement;
let rules: CSSStyleRule[];
let allRules: CSSRule[];

beforeAll(async () => {
  installed = await installCompiledCSS("src/ui/skeleton.css");
  rules = styleRules(installed);
  allRules = flattenCSSRules(installed.sheet!.cssRules);
});

afterAll(() => {
  installed?.remove();
});

function mediaRules(condition: string): CSSStyleRule[] {
  const media = allRules.find(
    (rule): rule is CSSMediaRule => rule instanceof CSSMediaRule && rule.conditionText === condition,
  );
  if (!media) throw new Error(`missing media query: ${condition}`);
  return flattenCSSRules(media.cssRules).filter(
    (rule): rule is CSSStyleRule => rule instanceof CSSStyleRule,
  );
}

describe("skeleton style contract", () => {
  it("disables the pulse animation under prefers-reduced-motion", () => {
    const reducedMotion = requireStyleRule(
      mediaRules("(prefers-reduced-motion: reduce)"),
      ".nx-skeleton",
      "animation",
    );
    expect(reducedMotion.style.getPropertyValue("animation")).toBe("none");
  });

  it("defines pulse keyframes and derives the bar color from the theme foreground", () => {
    const base = requireStyleRule(rules, ".nx-skeleton", "background");
    expect(base.style.getPropertyValue("display")).toBe("block");
    expect(base.style.getPropertyValue("background")).toContain("color-mix(");
    expect(base.style.getPropertyValue("background")).toContain("var(--nx-fg)");
    expect(base.style.getPropertyValue("animation")).toContain("nx-skeleton-pulse");

    const keyframes = allRules.find(
      (rule): rule is CSSKeyframesRule =>
        rule instanceof CSSKeyframesRule && rule.name === "nx-skeleton-pulse",
    );
    expect(keyframes).toBeDefined();
    expect(keyframes?.cssRules.length).toBeGreaterThan(0);
  });

  it("keeps skeleton bars distinguishable in forced-colors mode", () => {
    const forcedColors = requireStyleRule(
      mediaRules("(forced-colors: active)"),
      ".nx-skeleton",
      "background",
    );
    expect(forcedColors.style.getPropertyValue("background").toLowerCase()).toBe("canvastext");
    expect(Number(forcedColors.style.getPropertyValue("opacity"))).toBeGreaterThan(0);
  });
});
