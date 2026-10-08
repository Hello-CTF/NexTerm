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

describe("行内操作条的几何稳定性（CSS 机制）", () => {
  it("常驻行内操作条占位布局，只用 visibility 控制显隐", () => {
    const base = ruleBlock(".nx-row-reserve-actions .nx-row-actions");
    expect(base).toMatch(/display:\s*flex/);
    expect(base).toMatch(/visibility:\s*hidden/);
  });

  it("hover / 选中 / focus-within 只切 visibility，不回退 display 切换", () => {
    expect(css).toMatch(
      /\.nx-row-reserve-actions:hover \.nx-row-actions,\s*\.nx-row-reserve-actions\.is-selected \.nx-row-actions,\s*\.nx-row-reserve-actions:focus-within \.nx-row-actions\s*\{/,
    );
    const reveal = ruleBlock(".nx-row-reserve-actions:hover .nx-row-actions");
    expect(reveal).toMatch(/visibility:\s*visible/);
    expect(reveal).not.toMatch(/display:/);
  });

  it("coarse pointer 下常驻行内操作条常显，行高不再随交互变化", () => {
    const coarse = css.slice(css.indexOf("@media (pointer: coarse)"));
    const start = coarse.indexOf(".nx-row-reserve-actions .nx-row-actions");
    expect(start).toBeGreaterThan(0);
    const open = coarse.indexOf("{", start);
    const body = coarse.slice(open + 1, coarse.indexOf("}", open));
    expect(body).toMatch(/visibility:\s*visible/);
  });

  it("旧的 display 切换显隐路径与文件侧特判不再保留", () => {
    expect(css).not.toContain(".nx-files-row");
    expect(css).not.toMatch(/\.nx-row:hover \.nx-row-actions/);
  });
});
