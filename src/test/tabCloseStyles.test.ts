import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");

describe("tab close button layout stability", () => {
  it("keeps a permanent placeholder instead of display toggling", () => {
    const base = css.match(/\.nx-tab-close\s*\{([^}]*)\}/);
    expect(base).not.toBeNull();
    expect(base![1]).toMatch(/display:\s*inline-flex/);
    expect(base![1]).toMatch(/visibility:\s*hidden/);
    expect(base![1]).not.toMatch(/display:\s*none/);
  });

  it("reveals on hover, active tab and keyboard focus-within without touching display", () => {
    const reveal = css.match(
      /\.nx-tab:hover \.nx-tab-close,[\s\S]*?\.nx-tab:focus-within \.nx-tab-close\s*\{([^}]*)\}/,
    );
    expect(reveal).not.toBeNull();
    expect(reveal![1]).toMatch(/visibility:\s*visible/);
    expect(reveal![1]).not.toMatch(/display:/);
  });

  it("keeps the enlarged coarse-pointer target size", () => {
    const coarse = css.match(/@media \(pointer: coarse\) \{([\s\S]*?)\n\}/);
    expect(coarse).not.toBeNull();
    const rule = coarse![1].match(/\.nx-tab-close\s*\{([^}]*)\}/);
    expect(rule).not.toBeNull();
    expect(rule![1]).toMatch(/width:\s*44px/);
    expect(rule![1]).toMatch(/height:\s*44px/);
  });
});
