import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("../../styles.css", import.meta.url), "utf8");

function ruleBlock(selector: string): string {
  const start = css.indexOf(selector);
  if (start < 0) throw new Error(`selector not found: ${selector}`);
  const open = css.indexOf("{", start);
  if (open < 0) throw new Error(`block not found: ${selector}`);
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === "{") depth++;
    else if (css[i] === "}") {
      depth--;
      if (depth === 0) return css.slice(open + 1, i);
    }
  }
  throw new Error(`unbalanced block: ${selector}`);
}

describe("row action bar geometric stability (CSS mechanics)", () => {
  it("reserves layout for persistent row actions and toggles only visibility", () => {
    const base = ruleBlock(".nx-row-reserve-actions .nx-row-actions");
    expect(base).toMatch(/display:\s*flex/);
    expect(base).toMatch(/visibility:\s*hidden/);
  });

  it("hover, selected, and focus-within states toggle only visibility without display switching", () => {
    expect(css).toMatch(
      /\.nx-row-reserve-actions:hover \.nx-row-actions,\s*\.nx-row-reserve-actions\.is-selected \.nx-row-actions,\s*\.nx-row-reserve-actions:focus-within \.nx-row-actions\s*\{/,
    );
    const reveal = ruleBlock(".nx-row-reserve-actions:hover .nx-row-actions");
    expect(reveal).toMatch(/visibility:\s*visible/);
    expect(reveal).not.toMatch(/display:/);
  });

  it("keeps persistent row actions visible for coarse pointers without changing row height during interaction", () => {
    const coarse = css.slice(css.indexOf("@media (pointer: coarse)"));
    const start = coarse.indexOf(".nx-row-reserve-actions .nx-row-actions");
    expect(start).toBeGreaterThan(0);
    const open = coarse.indexOf("{", start);
    const body = coarse.slice(open + 1, coarse.indexOf("}", open));
    expect(body).toMatch(/visibility:\s*visible/);
  });

  it("removes legacy display-based visibility switching and file-specific rules", () => {
    expect(css).not.toContain(".nx-files-row");
    expect(css).not.toMatch(/\.nx-row:hover \.nx-row-actions/);
  });

  it("wraps long asset names without hiding overflow or applying ellipsis", () => {
    const name = ruleBlock(".nx-asset-row-name");
    expect(name).toMatch(/overflow-wrap:\s*anywhere/);
    expect(name).toMatch(/white-space:\s*normal/);
    expect(name).not.toMatch(/overflow:\s*hidden/);
    expect(name).not.toMatch(/text-overflow:/);
  });
});
