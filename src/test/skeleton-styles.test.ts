import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("../ui/skeleton.css", import.meta.url), "utf8");

describe("skeleton.css contract", () => {
  it("disables the pulse animation under prefers-reduced-motion", () => {
    const block = css.match(/@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}/);
    expect(block).not.toBeNull();
    expect(block![1]).toContain(".nx-skeleton");
    expect(block![1]).toMatch(/animation:\s*none/);
  });

  it("defines pulse keyframes and adapts the background to theme variables", () => {
    expect(css).toContain("@keyframes nx-skeleton-pulse");
    expect(css).toMatch(/background:\s*color-mix\(in srgb, var\(--nx-fg\)/);
  });

  it("keeps skeleton bars distinguishable in forced-colors mode", () => {
    const block = css.match(/@media \(forced-colors: active\) \{([\s\S]*?)\n\}/);
    expect(block).not.toBeNull();
    expect(block![1]).toContain("CanvasText");
  });

  it("imports skeleton.css in all four consumer components", () => {
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
