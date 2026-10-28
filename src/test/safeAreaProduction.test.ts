/** @vitest-environment jsdom */

import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { build } from "vite";
import { flattenCSSRules } from "./lib/cssom";

let installed: HTMLStyleElement;

beforeAll(async () => {
  const result = await build({ logLevel: "silent", build: { write: false } });
  const outputs = Array.isArray(result) ? result : [result];
  const css = outputs.flatMap((output) => {
    if (!("output" in output)) return [];
    return output.output.flatMap((entry) => {
      if (entry.type !== "asset" || !entry.fileName.endsWith(".css")) return [];
      const source = typeof entry.source === "string"
        ? entry.source
        : new TextDecoder().decode(entry.source);
      return [source];
    });
  }).join("\n");
  if (!css) throw new Error("production CSS asset not found");
  installed = document.createElement("style");
  installed.textContent = css;
  document.head.append(installed);
}, 120000);

beforeEach(() => {
  document.body.replaceChildren();
});

afterAll(() => {
  installed?.remove();
  document.body.replaceChildren();
});

describe("production safe-area cascade", () => {
  it("keeps the sheet background at the viewport edge with exactly one content inset", () => {
    document.body.innerHTML = `
      <div class="nx-overlay nx-menu-sheet-overlay" id="sheet-overlay">
        <div class="nx-menu-sheet" id="sheet"></div>
      </div>
      <div class="nx-overlay" id="normal-overlay"></div>
    `;

    const sheetOverlay = getComputedStyle(document.getElementById("sheet-overlay")!);
    expect(sheetOverlay.alignItems).toBe("flex-end");
    expect(sheetOverlay.paddingBottom).toBe("0px");
    expect(getComputedStyle(document.getElementById("sheet")!).paddingBottom).toBe("var(--nx-safe-bottom)");

    const normalPadding = getComputedStyle(document.getElementById("normal-overlay")!).paddingBottom;
    expect(normalPadding).toContain("24px");
    expect(normalPadding).toContain("--nx-kb-inset");
    expect(normalPadding).toContain("--nx-safe-bottom");
  });

  it("applies independent bottom insets to fixed and public fullscreen surfaces", () => {
    document.body.innerHTML = `
      <div class="nx-fullscreen-safe-bottom fixed inset-0" id="host-share"></div>
      <div id="root"><div class="flex h-dvh" id="public-share"><main></main></div></div>
      <div class="nx-auth-gate fixed inset-0 p-4" id="auth-gate">
        <div class="nx-card" id="auth-card"></div>
      </div>
    `;

    expect(getComputedStyle(document.getElementById("host-share")!).paddingBottom).toBe("var(--nx-safe-bottom)");
    expect(getComputedStyle(document.getElementById("public-share")!).paddingBottom).toBe("var(--nx-safe-bottom)");

    const authPadding = getComputedStyle(document.getElementById("auth-gate")!).paddingBottom;
    expect(authPadding).toContain("1rem");
    expect(authPadding).toContain("--nx-safe-bottom");
    const card = getComputedStyle(document.getElementById("auth-card")!);
    expect(card.maxHeight).toBe("100%");
    expect(card.overflowY).toBe("auto");
  });

  it("defines safe max-height and scrolling for main menus and submenus", () => {
    const rules = flattenCSSRules(installed.sheet!.cssRules).filter(
      (rule): rule is CSSStyleRule => rule instanceof CSSStyleRule,
    );
    for (const selector of [".nx-menu", ".nx-submenu"]) {
      const rule = rules.find((candidate) =>
        candidate.selectorText.split(",").some((part) => part.trim() === selector) &&
        candidate.style.getPropertyValue("max-height").includes("100dvh"),
      );
      expect(rule, selector).toBeDefined();
      expect(rule?.style.getPropertyValue("max-height")).toContain("16px");
      expect(rule?.style.getPropertyValue("max-height")).toContain("--nx-safe-bottom");
      expect(rule?.style.getPropertyValue("overflow-y")).toBe("auto");
    }
  });
});
