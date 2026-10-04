#!/usr/bin/env node
/**
 * 把 `website/diagrams/*.svg` 渲染成 `website/images/*.png`。
 *
 * 为什么不直接把 SVG 塞进 README / 官网：
 *   · README 引用的图与官网共用同一份资产，官网的 Pages 流水线只抓
 *     `website/images/*.png`（见 .github/workflows/pages.yml），PNG 是两边的公约数；
 *   · 图是**扁平色块 + 文字**，PNG 体积很小，且不会被各家 Markdown 渲染器的
 *     SVG 安全策略（脚本/外链）挡掉。
 *
 * SVG 是**源文件**（进版本库，改图改它），PNG 是**产物**（也进版本库，因为
 * 官网/README 直接引它）。两者都要提交。
 *
 * 依赖：本机装了 Chrome。用 `--headless --screenshot` 渲染，不装任何图形库
 * （本机没有 rsvg-convert / ImageMagick / cairosvg —— 别去装，Chrome 够用）。
 *
 * 用法::
 *
 *     node scripts/render-diagrams.mjs              # 全量渲染
 *     node scripts/render-diagrams.mjs 01-dual-form # 只渲染一张（文件名前缀）
 *     node scripts/render-diagrams.mjs --scale 3    # 换倍率（默认 2）
 */

import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const REPO = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const SRC_DIR = join(REPO, "website", "diagrams");
const OUT_DIR = join(REPO, "website", "images");

const CHROME_CANDIDATES = [
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  "/Applications/Chromium.app/Contents/MacOS/Chromium",
  "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
  "/usr/bin/google-chrome",
  "/usr/bin/chromium",
];

function findChrome() {
  for (const p of CHROME_CANDIDATES) {
    if (existsSync(p)) return p;
  }
  throw new Error(
    "找不到 Chrome/Chromium。装一个，或把路径加进 CHROME_CANDIDATES。"
  );
}

/** 从 SVG 根标签上读 width/height，作为截图窗口尺寸。 */
function svgSize(file) {
  const head = readFileSync(file, "utf8").slice(0, 2000);
  const w = head.match(/\bwidth="(\d+)"/);
  const h = head.match(/\bheight="(\d+)"/);
  if (!w || !h) throw new Error(`${file}: 根 <svg> 上没写 width/height`);
  return { w: Number(w[1]), h: Number(h[1]) };
}

function main() {
  const args = process.argv.slice(2);
  const scaleIdx = args.indexOf("--scale");
  const scale = scaleIdx >= 0 ? Number(args[scaleIdx + 1]) : 2;
  const filters = args.filter((a) => !a.startsWith("--") && a !== String(scale));

  if (!existsSync(SRC_DIR)) throw new Error(`没有 ${SRC_DIR}`);
  mkdirSync(OUT_DIR, { recursive: true });

  const chrome = findChrome();
  const svgs = readdirSync(SRC_DIR)
    .filter((f) => f.endsWith(".svg") && !f.startsWith("_"))
    .filter((f) => filters.length === 0 || filters.some((p) => f.startsWith(p)))
    .sort();

  if (svgs.length === 0) {
    console.log("没有匹配的 SVG。");
    return;
  }

  let ok = 0;
  for (const name of svgs) {
    const src = join(SRC_DIR, name);
    const { w, h } = svgSize(src);
    const dst = join(OUT_DIR, name.replace(/\.svg$/, ".png"));

    // ⚠️ `--window-size` 用的是 CSS 像素，`--force-device-scale-factor` 才是倍率 ——
    // 两者相乘得到真实像素，所以这里传的是逻辑尺寸，不是 w*scale。
    execFileSync(
      chrome,
      [
        "--headless",
        "--disable-gpu",
        "--no-sandbox",
        "--hide-scrollbars",
        "--default-background-color=00000000",
        `--force-device-scale-factor=${scale}`,
        `--window-size=${w},${h}`,
        `--screenshot=${dst}`,
        `file://${src}`,
      ],
      { stdio: ["ignore", "ignore", "ignore"] }
    );

    const kb = (statSync(dst).size / 1024).toFixed(1);
    console.log(`[ok] ${name} -> website/images/${name.replace(/\.svg$/, ".png")}  ${w * scale}x${h * scale}  ${kb} KB`);
    ok += 1;
  }
  console.log(`\n${ok} 张 -> website/images/`);
}

main();
