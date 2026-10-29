/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { declared, installCompiledCSS, styleRules } from "../lib/cssom";

describe("one-line tree rows on touch screens", () => {
  it("share one minimum height, so rows with in-flow and absolute action buttons match", async () => {
    const installed = await installCompiledCSS("src/styles.css");
    try {
      const rules = styleRules(installed);
      expect(declared(rules, ".nx-row-oneline", "min-height", "(pointer: coarse)")).toBe("40px");
      // The in-flow actions of an asset row are at most 36px tall, so they fit the shared height.
      expect(declared(rules, ".nx-row-actions .nx-icon-btn-sm, .nx-icon-btn-sm.nx-row-more", "height", "(pointer: coarse)")
        ?? declared(rules, ".nx-icon-btn-sm.nx-row-more", "height", "(pointer: coarse)")).toBe("36px");
      // A fine pointer keeps the content-height rows.
      expect(declared(rules, ".nx-row-oneline", "min-height", null)).toBeUndefined();
    } finally {
      installed.remove();
    }
  });
});
