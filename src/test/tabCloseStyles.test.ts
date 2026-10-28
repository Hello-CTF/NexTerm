/** @vitest-environment jsdom */

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { declared, installCompiledCSS, rulesFor, styleRules } from "./lib/cssom";

let installed: HTMLStyleElement;
let rules: CSSStyleRule[];

beforeAll(async () => {
  installed = await installCompiledCSS("src/styles.css");
  rules = styleRules(installed);
});

afterAll(() => installed?.remove());

describe("tab close button layout stability", () => {
  it("keeps a permanent placeholder instead of display toggling", () => {
    const [base] = rulesFor(rules, ".nx-tab-close");
    expect(base.style.getPropertyValue("display")).toBe("inline-flex");
    expect(base.style.getPropertyValue("visibility")).toBe("hidden");
  });

  it("reveals on hover, active tab and keyboard focus-within without touching display", () => {
    for (const selector of [
      ".nx-tab:hover .nx-tab-close",
      ".nx-tab.is-active .nx-tab-close",
      ".nx-tab:focus-within .nx-tab-close",
    ]) {
      const [reveal] = rulesFor(rules, selector);
      expect(reveal, selector).toBeDefined();
      expect(reveal.style.getPropertyValue("visibility")).toBe("visible");
      expect(reveal.style.getPropertyValue("display")).toBe("");
    }
  });

  it("keeps the enlarged coarse-pointer target size", () => {
    expect(declared(rules, ".nx-tab-close", "width", "(pointer: coarse)")).toBe("44px");
    expect(declared(rules, ".nx-tab-close", "height", "(pointer: coarse)")).toBe("44px");
  });
});
