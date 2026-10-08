#!/usr/bin/env node
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
  const fromEnv = process.env.CHROME;
  if (fromEnv && existsSync(fromEnv)) return fromEnv;
  for (const p of CHROME_CANDIDATES) {
    if (existsSync(p)) return p;
  }
  throw new Error(
    "找不到 Chrome/Chromium。装一个，用 CHROME 环境变量指定路径，或把路径加进 CHROME_CANDIDATES。"
  );
}

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
