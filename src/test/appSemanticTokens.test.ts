import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const app = readFileSync(new URL("../app/App.tsx", import.meta.url), "utf8");

// 不能用 \b 单词边界：Tailwind 任意值里颜色前一个字符常是 `_`
// （如 shadow-[…_rgba(0,0,0,0.75)]），下划线与 r 都是单词字符，
// 二者之间没有边界，/\brgba?\(/ 会漏报。
const RAW_HEX = /#[0-9a-fA-F]{3,8}/;
const RAW_RGB = /rgba?\(/;

describe("App-level semantic tokens", () => {
  it("keeps App.tsx free of raw color literals", () => {
    expect(app).not.toMatch(RAW_HEX);
    expect(app).not.toMatch(RAW_RGB);
  });

  it("guard rejects raw colors inside Tailwind arbitrary values", () => {
    const samples = [
      "shadow-[0_18px_48px_-16px_rgba(0,0,0,0.75)]",
      "bg-[rgb(1,2,3)]",
      "text-[#a1b2c3]/80",
      "border-[#abc]",
    ];
    for (const sample of samples) {
      expect(RAW_RGB.test(sample) || RAW_HEX.test(sample), sample).toBe(true);
    }
  });

  it("styles the success toast from theme tokens", () => {
    expect(app).toContain("bg-[color-mix(in_srgb,var(--color-green-500)_18%,var(--nx-bg-pane))]");
  });

  it("references the runtime shadow token instead of the statically expanded utility", () => {
    // shadow-pop 工具类会把 @theme 暗色值静态展开进生成 CSS，运行时亮主题覆盖不生效；
    // 必须保留 var() 引用的任意值形式（生成 --tw-shadow: var(--shadow-pop)）。
    expect(app).toContain("shadow-[var(--shadow-pop)]");
    expect(app).not.toMatch(/["' ]shadow-pop["' ]/);
  });
});
