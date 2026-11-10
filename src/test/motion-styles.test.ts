/** @vitest-environment jsdom */

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { flattenCSSRules, installCompiledCSS, requireStyleRule, styleRules } from "./lib/cssom";

let motion: HTMLStyleElement;
let a11y: HTMLStyleElement;
let tokens: HTMLStyleElement;

beforeAll(async () => {
  [motion, a11y, tokens] = await Promise.all([
    installCompiledCSS("src/styles/motion.css"),
    installCompiledCSS("src/ui/a11y.css"),
    installCompiledCSS("src/styles/tokens.css"),
  ]);
});

afterAll(() => {
  motion?.remove();
  a11y?.remove();
  tokens?.remove();
});

const SURFACES = [".nx-overlay", ".nx-modal", ".nx-menu", ".nx-popover", ".nx-toast"];

function reducedMotionRules(): CSSStyleRule[] {
  const media = flattenCSSRules(a11y.sheet!.cssRules).find(
    (rule): rule is CSSMediaRule =>
      rule instanceof CSSMediaRule && rule.conditionText === "(prefers-reduced-motion: reduce)",
  );
  if (!media) throw new Error("missing reduced-motion media query");
  return flattenCSSRules(media.cssRules).filter(
    (rule): rule is CSSStyleRule => rule instanceof CSSStyleRule,
  );
}

describe("motion style contract", () => {
  it("defines the motion tokens", () => {
    const root = requireStyleRule(styleRules(tokens), ":root", "--nx-dur-fast");
    for (const token of ["--nx-ease-out", "--nx-ease-in-out", "--nx-dur-fast", "--nx-dur-base"]) {
      expect(root.style.getPropertyValue(token).trim(), token).not.toBe("");
    }
  });

  it("animates floating surfaces in with the motion tokens and toasts out", () => {
    const rules = styleRules(motion);
    for (const selector of SURFACES) {
      const animation = requireStyleRule(rules, selector, "animation").style.getPropertyValue("animation");
      expect(animation, selector).toMatch(/var\(--nx-dur-(fast|base)\)/);
    }
    const leaving = requireStyleRule(rules, ".nx-toast.is-leaving", "animation");
    expect(leaving.style.getPropertyValue("animation")).toContain("nx-toast-out");
  });

  it("turns every surface animation off and hides the countdown under prefers-reduced-motion", () => {
    const rules = reducedMotionRules();
    for (const selector of [...SURFACES, ".nx-toast.is-leaving", ".nx-modal.nx-command-modal", ".nx-submenu"]) {
      const rule = requireStyleRule(rules, selector, "animation");
      expect(rule.style.getPropertyValue("animation"), selector).toBe("none");
    }
    const progress = requireStyleRule(rules, ".nx-toast-progress", "display");
    expect(progress.style.getPropertyValue("display")).toBe("none");
  });
});
