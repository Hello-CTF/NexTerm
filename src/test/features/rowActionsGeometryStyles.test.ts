/** @vitest-environment jsdom */

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { declared, installCompiledCSS, rulesFor, styleRules } from "../lib/cssom";

let installed: HTMLStyleElement;
let rules: CSSStyleRule[];

beforeAll(async () => {
  installed = await installCompiledCSS("src/styles.css");
  rules = styleRules(installed);
});

afterAll(() => installed?.remove());

describe("row action bar geometric stability (CSS mechanics)", () => {
  it("reserves layout for persistent row actions and toggles only visibility", () => {
    const selector = ".nx-row-reserve-actions .nx-row-actions";
    expect(declared(rules, selector, "display")).toBe("flex");
    expect(rulesFor(rules, selector)[0].style.getPropertyValue("visibility")).toBe("hidden");
  });

  it("hover, selected, and focus-within states toggle only visibility without display switching", () => {
    for (const selector of [
      ".nx-row-reserve-actions:hover .nx-row-actions",
      ".nx-row-reserve-actions.is-selected .nx-row-actions",
      ".nx-row-reserve-actions:focus-within .nx-row-actions",
    ]) {
      const [reveal] = rulesFor(rules, selector);
      expect(reveal, selector).toBeDefined();
      expect(reveal.style.getPropertyValue("visibility")).toBe("visible");
      expect(reveal.style.getPropertyValue("display")).toBe("");
    }
  });

  it("keeps persistent row actions visible for coarse pointers without changing row height during interaction", () => {
    expect(
      declared(
        rules,
        ".nx-row-reserve-actions .nx-row-actions",
        "visibility",
        "(pointer: coarse)",
      ),
    ).toBe("visible");
  });

  it("removes legacy display-based visibility switching and file-specific rules", () => {
    const selectors = rules.flatMap((rule) =>
      rule.selectorText.split(",").map((part) => part.trim()),
    );
    expect(selectors.filter((selector) => selector.includes(".nx-files-row"))).toEqual([]);
    expect(selectors).not.toContain(".nx-row:hover .nx-row-actions");
  });

  it("keeps long asset names on one horizontally scrollable line without applying ellipsis", () => {
    const scroll = rulesFor(rules, ".nx-asset-row-name").find(
      (rule) => rule.style.getPropertyValue("overflow-x").trim() === "auto",
    );
    expect(scroll).toBeDefined();
    expect(scroll!.style.getPropertyValue("overflow-y")).toBe("hidden");
    expect(scroll!.style.getPropertyValue("white-space")).toBe("nowrap");
    for (const rule of rulesFor(rules, ".nx-asset-row-name")) {
      expect(rule.style.getPropertyValue("overflow-wrap")).not.toBe("anywhere");
      expect(rule.style.getPropertyValue("text-overflow")).not.toBe("ellipsis");
    }
  });
});
