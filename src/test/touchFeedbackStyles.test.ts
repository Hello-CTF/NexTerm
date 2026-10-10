import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");

function blockAfter(marker: string): string {
  const start = css.indexOf(marker);
  if (start < 0) throw new Error(`marker not found: ${marker}`);
  const open = css.indexOf("{", start);
  if (open < 0) throw new Error(`block not found: ${marker}`);
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === "{") depth++;
    else if (css[i] === "}") {
      depth--;
      if (depth === 0) return css.slice(open + 1, i);
    }
  }
  throw new Error(`unbalanced block: ${marker}`);
}

function rule(body: string, selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = body.match(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`));
  if (!match) throw new Error(`rule not found: ${selector}`);
  return match[1];
}

const coarse = blockAfter("@media (pointer: coarse) {");
const narrow = blockAfter("@media (max-width: 820px) {");

describe("coarse pointer hit areas", () => {
  it("rail buttons are at least 44x44", () => {
    const body = rule(coarse, ".nx-rail-btn");
    expect(body).toMatch(/width:\s*44px/);
    expect(body).toMatch(/height:\s*44px/);
  });

  it("terminal key bar buttons are at least 44x44 and the bar grows to fit", () => {
    const key = rule(coarse, ".nx-terminal-key");
    expect(key).toMatch(/min-width:\s*44px/);
    expect(key).toMatch(/height:\s*44px/);
    const bar = rule(coarse, ".nx-terminal-keys");
    expect(bar).toMatch(/min-height:\s*52px/);
  });

  it("row action buttons are real non-overlapping 44x44 hit areas on their own row", () => {
    expect(rule(coarse, ".nx-row")).toMatch(/flex-wrap:\s*wrap/);
    const actions = rule(coarse, ".nx-row-actions");
    expect(actions).toMatch(/gap:\s*8px/);
    expect(actions).toMatch(/flex-basis:\s*100%/);
    expect(actions).toMatch(/flex-wrap:\s*wrap/);
    expect(coarse).toMatch(/\.nx-row-actions \.nx-icon-btn-sm,\s*\.nx-icon-btn-sm\.nx-row-more\s*\{/);
    const btn = rule(coarse, ".nx-icon-btn-sm.nx-row-more");
    expect(btn).toMatch(/width:\s*44px/);
    expect(btn).toMatch(/height:\s*44px/);
    expect(btn).not.toMatch(/margin:/);
  });

  it("all icon buttons and small controls are at least 44x44", () => {
    for (const selector of [".nx-icon-btn", ".nx-icon-btn-sm", ".nx-tab-close", ".nx-tab-new"]) {
      const body = rule(coarse, selector);
      expect(body).toMatch(/width:\s*44px/);
      expect(body).toMatch(/height:\s*44px/);
    }
    for (const selector of [".nx-btn", ".nx-btn-sm", ".nx-btn-xs", ".nx-link"]) {
      expect(rule(coarse, selector)).toMatch(/min-height:\s*44px/);
    }
  });

  it("desktop keeps the compact 34px rail", () => {
    const base = rule(css, ".nx-rail-btn");
    expect(base).toMatch(/width:\s*34px/);
    expect(base).toMatch(/height:\s*34px/);
  });
});

describe("coarse pointer form controls", () => {
  it("inputs, selects and textareas meet the 44px convention under the utilities layer", () => {
    const layer = coarse.slice(coarse.indexOf("@layer components {"));
    expect(layer).toMatch(/\.nx-input,\s*\.nx-select\s*\{[^}]*min-height:\s*44px/);
    expect(layer).toMatch(
      /\.nx-textarea:not\(\.nx-textarea-grow\)\s*\{[^}]*min-height:\s*44px/,
    );
    expect(layer).toMatch(/\.nx-check\s*\{[^}]*width:\s*44px;[^}]*height:\s*44px/);
  });

  it("tabs, the workspace switcher, segment items and the AI send button meet 44px", () => {
    expect(coarse).toMatch(/\.nx-tab,\s*\.nx-ws-switch\s*\{[^}]*min-height:\s*44px/);
    expect(coarse).toMatch(/\.nx-segment-item\s*\{[^}]*min-height:\s*44px/);
    expect(coarse).toMatch(/\.nx-send-btn\s*\{[^}]*width:\s*44px;[^}]*height:\s*44px/);
  });

  it("path, tree and check controls meet 44px and the pathbar grows to fit", () => {
    expect(coarse).toMatch(/\.nx-tree-caret\s*\{[^}]*width:\s*44px;[^}]*height:\s*44px/);
    expect(coarse).toMatch(/\.nx-pathbar\s*\{[^}]*height:\s*auto;[^}]*min-height:\s*44px/);
    expect(coarse).toMatch(/\.nx-path-crumb\s*\{[^}]*min-height:\s*44px/);
  });

  it("desktop keeps the compact control dimensions", () => {
    const fields = css.match(/\.nx-input,\s*\.nx-select,\s*\.nx-textarea\s*\{([^}]*)\}/);
    expect(fields).not.toBeNull();
    expect(fields![1]).toMatch(/height:\s*30px/);
    expect(css).toMatch(/\.nx-tab\s*\{[^}]*height:\s*28px/);
    expect(css).toMatch(/\.nx-ws-switch\s*\{[^}]*height:\s*26px/);
    expect(rule(css, ".nx-segment-item")).toMatch(/height:\s*24px/);
    expect(rule(css, ".nx-send-btn")).toMatch(/width:\s*30px/);
    expect(rule(css, ".nx-send-btn")).toMatch(/height:\s*30px/);
    expect(rule(css, ".nx-check")).toMatch(/width:\s*14px/);
    expect(rule(css, ".nx-tree-caret")).toMatch(/width:\s*14px/);
  });
});

describe("reduced motion coverage", () => {
  it("stops spin and dot-pulse animations", () => {
    const block = blockAfter("@media (prefers-reduced-motion: reduce) {");
    expect(block).toMatch(/\.animate-spin,\s*\.nx-dot-pulse\s*\{[^}]*animation:\s*none/);
  });
});

describe("toast placement rules", () => {
  it("keys-aware bottom offset goes through the shared keys height variable", () => {
    const body = rule(css, '.nx-app[data-nx-keys="true"] .nx-toasts');
    expect(body).toMatch(/var\(--nx-keys-h/);
    expect(css).toMatch(/--nx-kb-inset:\s*0px;\s*--nx-keys-h:\s*46px;/);
    expect(rule(coarse, ":root")).toMatch(/--nx-keys-h:\s*53px/);
  });

  it("toasts shift left of the docked AI sidebar within the remaining space", () => {
    const body = rule(css, '.nx-app[data-nx-ai="dock"] .nx-toasts');
    expect(body).toMatch(/right:\s*calc\(var\(--nx-right-w/);
    expect(body).toMatch(/width:\s*min\(380px, calc\(100vw - var\(--nx-right-w/);
  });

  it("falls back to the top-left edge when the docked sidebar leaves too little room", () => {
    const body = rule(css, '.nx-app[data-nx-ai="left"] .nx-toasts');
    expect(body).toMatch(/left:\s*8px/);
    expect(body).toMatch(/right:\s*auto/);
    expect(body).toMatch(/bottom:\s*auto/);
  });

  it("caps the stack above the AI input via the measured inset variable", () => {
    for (const body of [
      rule(css, '.nx-app[data-nx-ai="left"] .nx-toasts'),
      rule(narrow, ".nx-app[data-nx-ai] .nx-toasts"),
    ]) {
      expect(body).toMatch(/max-height:\s*min\(/);
      expect(body).toMatch(/var\(--nx-ai-toast-max/);
    }
  });

  it("toast top offset follows the shared header stack variable", () => {
    expect(css).toMatch(/--nx-keys-h:\s*46px;\s*--nx-header-stack:\s*36px;/);
    expect(rule(coarse, ":root")).toMatch(/--nx-header-stack:\s*47px/);
    for (const body of [
      rule(css, '.nx-app[data-nx-ai="left"] .nx-toasts'),
      rule(narrow, ".nx-app[data-nx-ai] .nx-toasts"),
    ]) {
      expect(body).toMatch(/top:\s*calc\(var\(--nx-header-stack\) \+ 8px\)/);
    }
  });

  it("on narrow screens toasts move to the top-left so the AI input stays reachable", () => {
    const body = rule(narrow, ".nx-app[data-nx-ai] .nx-toasts");
    expect(body).toMatch(/left:\s*8px/);
    expect(body).toMatch(/right:\s*auto/);
    expect(body).toMatch(/bottom:\s*auto/);
    expect(body).toMatch(/top:\s*calc\(var\(--nx-header-stack\) \+ 8px\)/);
  });

  it("toast stack is constrained in height and keeps the newest toasts", () => {
    const body = rule(css, ".nx-toasts");
    expect(body).toMatch(/max-height:/);
    expect(body).toMatch(/overflow:\s*hidden/);
    expect(body).toMatch(/justify-content:\s*flex-end/);
  });
});

describe("workspace status styles", () => {
  it("status icon tones are defined for every state", () => {
    const base = rule(css, ".nx-ws-status");
    expect(base).toMatch(/display:\s*inline-flex/);
    expect(rule(css, ".nx-ws-status.is-ok")).toMatch(/color:/);
    expect(rule(css, ".nx-ws-status.is-warn")).toMatch(/color:/);
    expect(rule(css, ".nx-ws-status.is-bad")).toMatch(/color:/);
  });
});
