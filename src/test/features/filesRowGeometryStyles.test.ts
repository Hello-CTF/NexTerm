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

describe("文件行操作条的几何稳定性（CSS 机制）", () => {
  it("操作条常驻布局（reserved space），只用 visibility 控制显隐", () => {
    const base = ruleBlock(".nx-files-row .nx-row-actions");
    expect(base).toMatch(/display:\s*flex/);
    expect(base).toMatch(/visibility:\s*hidden/);
  });

  it("hover / 选中 / focus-within 只切 visibility，不回退 display 切换", () => {
    expect(css).toMatch(
      /\.nx-files-row:hover \.nx-row-actions,\s*\.nx-files-row\.is-selected \.nx-row-actions,\s*\.nx-files-row:focus-within \.nx-row-actions\s*\{/,
    );
    const reveal = ruleBlock(".nx-files-row:hover .nx-row-actions");
    expect(reveal).toMatch(/visibility:\s*visible/);
    expect(reveal).not.toMatch(/display:/);
  });

  it("共享的 .nx-row-actions 默认隐藏规则保持原样（资产树/片段不受影响）", () => {
    expect(css).toMatch(/\.nx-row-actions\s*\{\s*display:\s*none/);
    expect(css).toMatch(
      /\.nx-row:hover \.nx-row-actions,\s*\.nx-row\.is-selected \.nx-row-actions\s*\{\s*display:\s*flex/,
    );
  });
});
