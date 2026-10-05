import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("../ui/skeleton.css", import.meta.url), "utf8");

describe("skeleton.css 契约", () => {
  it("脉冲动画在 prefers-reduced-motion 下停用", () => {
    const block = css.match(/@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}/);
    expect(block).not.toBeNull();
    expect(block![1]).toContain(".nx-skeleton");
    expect(block![1]).toMatch(/animation:\s*none/);
  });

  it("定义脉冲 keyframes 且背景随主题变量自适应", () => {
    expect(css).toContain("@keyframes nx-skeleton-pulse");
    expect(css).toMatch(/background:\s*color-mix\(in srgb, var\(--nx-fg\)/);
  });

  it("forced-colors 下骨架条仍可辨识", () => {
    const block = css.match(/@media \(forced-colors: active\) \{([\s\S]*?)\n\}/);
    expect(block).not.toBeNull();
    expect(block![1]).toContain("CanvasText");
  });

  it("四个使用方组件都引入 skeleton.css", () => {
    const files = [
      "features/files/FileBrowser.tsx",
      "features/files/FileTree.tsx",
      "features/docker/DockerPanel.tsx",
      "features/terminal/BackgroundSessions.tsx",
    ];
    for (const file of files) {
      const src = readFileSync(new URL(`../${file}`, import.meta.url), "utf8");
      expect(src, file).toContain('import "../../ui/skeleton.css";');
    }
  });
});
