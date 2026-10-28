/** @vitest-environment jsdom */

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import {
  declared,
  installCompiledCSS,
  requireStyleRule,
  ruleLayers,
  rulesFor,
  styleRules,
} from "./lib/cssom";

const COARSE = "(pointer: coarse)";
const NARROW = "(max-width: 820px)";
const REDUCED_MOTION = "(prefers-reduced-motion: reduce)";

let installed: HTMLStyleElement;
let rules: CSSStyleRule[];

beforeAll(async () => {
  installed = await installCompiledCSS("src/styles.css");
  rules = styleRules(installed);
});

afterAll(() => installed?.remove());

const base = (selector: string, property: string) => declared(rules, selector, property);
const coarse = (selector: string, property: string) =>
  declared(rules, selector, property, COARSE);

describe("coarse pointer hit areas", () => {
  it("rail buttons are at least 44x44", () => {
    expect(coarse(".nx-rail-btn", "width")).toBe("44px");
    expect(coarse(".nx-rail-btn", "height")).toBe("44px");
  });

  it("terminal key bar buttons are at least 44x44 and the bar grows to fit", () => {
    expect(coarse(".nx-terminal-key", "min-width")).toBe("44px");
    expect(coarse(".nx-terminal-key", "height")).toBe("44px");
    expect(coarse(".nx-terminal-keys", "min-height")).toBe("52px");
  });

  it("row action buttons are real non-overlapping 44x44 hit areas on their own row", () => {
    expect(coarse(".nx-row", "flex-wrap")).toBe("wrap");
    expect(coarse(".nx-row-actions", "gap")).toBe("8px");
    expect(coarse(".nx-row-actions", "flex-basis")).toBe("100%");
    expect(coarse(".nx-row-actions", "flex-wrap")).toBe("wrap");
    // The row actions and the compact "more" button share one declaration.
    expect(rulesFor(rules, ".nx-row-actions .nx-icon-btn-sm", COARSE).length).toBeGreaterThan(0);
    const more = rulesFor(rules, ".nx-icon-btn-sm.nx-row-more", COARSE).find(
      (rule) => rule.style.getPropertyValue("width").trim() === "40px",
    );
    // Compact one-line rows: a 36px visible button whose ::after extends the hit area to 44px.
    expect(more).toBeDefined();
    expect(more!.style.getPropertyValue("height").trim()).toBe("36px");
    expect(more!.style.getPropertyValue("margin")).toBe("");
    const hitArea = rulesFor(rules, ".nx-icon-btn-sm.nx-row-more::after", COARSE);
    expect(hitArea.length).toBeGreaterThan(0);
  });

  it("all icon buttons and small controls are at least 44x44", () => {
    for (const selector of [".nx-icon-btn", ".nx-icon-btn-sm", ".nx-tab-close", ".nx-tab-new"]) {
      expect(coarse(selector, "width"), selector).toBe("44px");
      expect(coarse(selector, "height"), selector).toBe("44px");
    }
    for (const selector of [".nx-btn", ".nx-btn-sm", ".nx-btn-xs", ".nx-link"]) {
      expect(coarse(selector, "min-height"), selector).toBe("44px");
    }
  });

  it("desktop keeps the compact 34px rail", () => {
    expect(base(".nx-rail-btn", "width")).toBe("34px");
    expect(base(".nx-rail-btn", "height")).toBe("34px");
  });
});

describe("coarse pointer form controls", () => {
  it("inputs, selects and textareas meet the 44px convention under the utilities layer", () => {
    for (const selector of [".nx-input", ".nx-select", ".nx-textarea:not(.nx-textarea-grow)"]) {
      const rule = rulesFor(rules, selector, COARSE).find(
        (candidate) => candidate.style.getPropertyValue("min-height").trim() === "44px",
      );
      expect(rule, selector).toBeDefined();
      expect(ruleLayers(rule!)).toContain("components");
    }
    expect(coarse(".nx-check", "width")).toBe("44px");
    expect(coarse(".nx-check", "height")).toBe("44px");
  });

  it("tabs, the workspace switcher, segment items and the AI send button meet 44px", () => {
    expect(coarse(".nx-tab", "min-height")).toBe("44px");
    expect(coarse(".nx-ws-switch", "min-height")).toBe("44px");
    expect(coarse(".nx-segment-item", "min-height")).toBe("44px");
    expect(coarse(".nx-send-btn", "width")).toBe("44px");
    expect(coarse(".nx-send-btn", "height")).toBe("44px");
  });

  it("path, tree and check controls meet 44px and the pathbar grows to fit", () => {
    expect(coarse(".nx-tree-caret", "width")).toBe("40px");
    expect(coarse(".nx-tree-caret", "height")).toBe("36px");
    expect(coarse(".nx-pathbar", "height")).toBe("auto");
    expect(coarse(".nx-pathbar", "min-height")).toBe("44px");
    expect(coarse(".nx-path-crumb", "min-height")).toBe("44px");
  });

  it("desktop keeps the compact control dimensions", () => {
    expect(base(".nx-input", "height")).toBe("30px");
    expect(base(".nx-select", "height")).toBe("30px");
    // Textareas share the 30px declaration and then opt back into auto height for growth.
    expect(
      rulesFor(rules, ".nx-textarea").some(
        (rule) => rule.style.getPropertyValue("height").trim() === "30px",
      ),
    ).toBe(true);
    expect(base(".nx-tab", "height")).toBe("28px");
    expect(base(".nx-ws-switch", "height")).toBe("26px");
    expect(base(".nx-segment-item", "height")).toBe("24px");
    expect(base(".nx-send-btn", "width")).toBe("28px");
    expect(base(".nx-send-btn", "height")).toBe("28px");
    expect(base(".nx-check", "width")).toBe("14px");
    expect(base(".nx-tree-caret", "width")).toBe("14px");
  });
});

describe("reduced motion coverage", () => {
  it("stops spin and dot-pulse animations", () => {
    expect(declared(rules, ".animate-spin", "animation", REDUCED_MOTION)).toBe("none");
    expect(declared(rules, ".nx-dot-pulse", "animation", REDUCED_MOTION)).toBe("none");
  });
});

describe("toast placement rules", () => {
  const dock = '.nx-app[data-nx-ai="dock"] .nx-toasts';
  const left = '.nx-app[data-nx-ai="left"] .nx-toasts';
  const narrowAi = ".nx-app[data-nx-ai] .nx-toasts";
  const topOffset = "calc(var(--nx-header-stack) + 8px + var(--nx-safe-top))";

  it("keys-aware bottom offset goes through the shared keys height variable", () => {
    expect(base('.nx-app[data-nx-keys="true"] .nx-toasts', "bottom")).toContain("var(--nx-keys-h");
    expect(base(":root", "--nx-keys-h")).toBe("46px");
    expect(coarse(":root", "--nx-keys-h")).toBe("53px");
  });

  it("toasts shift left of the docked AI sidebar within the remaining space", () => {
    expect(base(dock, "right")).toContain("calc(var(--nx-right-w");
    expect(base(dock, "width")).toContain("min(380px, calc(100vw - var(--nx-right-w");
  });

  it("falls back to the top-left edge when the docked sidebar leaves too little room", () => {
    expect(base(left, "left")).toBe("8px");
    expect(base(left, "right")).toBe("auto");
    expect(base(left, "bottom")).toBe("auto");
  });

  it("caps the stack above the AI input via the measured inset variable", () => {
    for (const value of [
      base(left, "max-height"),
      declared(rules, narrowAi, "max-height", NARROW),
    ]) {
      expect(value).toMatch(/^min\(/);
      expect(value).toContain("var(--nx-ai-toast-max");
    }
  });

  it("toast top offset follows the shared header stack variable", () => {
    expect(base(":root", "--nx-header-stack")).toBe("36px");
    expect(coarse(":root", "--nx-header-stack")).toBe("47px");
    expect(base(left, "top")).toBe(topOffset);
    expect(declared(rules, narrowAi, "top", NARROW)).toBe(topOffset);
  });

  it("on narrow screens toasts move to the top-left so the AI input stays reachable", () => {
    expect(declared(rules, narrowAi, "left", NARROW)).toBe("8px");
    expect(declared(rules, narrowAi, "right", NARROW)).toBe("auto");
    expect(declared(rules, narrowAi, "bottom", NARROW)).toBe("auto");
    expect(declared(rules, narrowAi, "top", NARROW)).toBe(topOffset);
  });

  it("toast stack is constrained in height and keeps the newest toasts", () => {
    expect(base(".nx-toasts", "max-height")).toBeTruthy();
    expect(base(".nx-toasts", "overflow")).toBe("hidden");
    expect(base(".nx-toasts", "justify-content")).toBe("flex-end");
  });
});

describe("workspace status styles", () => {
  it("status icon tones are defined for every state", () => {
    expect(base(".nx-ws-status", "display")).toBe("inline-flex");
    for (const state of ["is-ok", "is-warn", "is-bad"]) {
      const rule = requireStyleRule(rules, `.nx-ws-status.${state}`, "color");
      expect(rule.style.getPropertyValue("color").trim()).not.toBe("");
    }
  });
});
