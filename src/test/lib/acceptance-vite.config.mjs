import base from "../../../vite.config.ts";

// 有界 dev 行为 (optimizeDeps.entries 钉唯一 HTML 入口 + root 锚定 watch.ignored)
// 已上移进仓库根 vite.config.ts, pnpm dev 与验收共用同一套规则; 此处不再二次合并
// (mergeConfig 拼接数组, 会让 ignored/entries 翻倍)。本文件保留为验收启动器的
// 稳定 config 锚点 (acceptance-process.mjs 的默认 --config)。
export default base;
