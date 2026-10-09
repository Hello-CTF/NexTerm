import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");
const a11y = readFileSync(new URL("../ui/a11y.css", import.meta.url), "utf8");

describe("focus-visible 契约", () => {
  it("命令面板输入自动聚焦,不绘制焦点环", () => {
    expect(a11y).toMatch(
      /\.nx-overlay \.nx-command-input:focus-visible\s*\{[^}]*outline:\s*none/,
    );
  });

  it("CodeMirror 编辑器聚焦时绘制焦点环", () => {
    expect(css).toMatch(
      /\.cm-editor\.cm-focused:focus-within\s*\{[^}]*outline:\s*2px solid var\(--nx-focus-ring\)[^}]*outline-offset:\s*-2px/,
    );
  });

  it("CodeMirror 面板输入聚焦时绘制焦点环", () => {
    expect(css).toMatch(
      /\.cm-editor \.cm-textfield:focus-visible\s*\{[^}]*outline:\s*2px solid var\(--nx-focus-ring\)/,
    );
  });

  it("forced-colors 下 overlay 焦点环仍映射到 Highlight", () => {
    const block = a11y.match(/@media \(forced-colors: active\) \{([\s\S]*?)\n\}/);
    expect(block).not.toBeNull();
    expect(block![1]).toMatch(/outline-color:\s*Highlight/);
  });
});
