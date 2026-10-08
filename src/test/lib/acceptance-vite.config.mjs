import { defineConfig, mergeConfig } from "vite";
import path from "node:path";
import { fileURLToPath } from "node:url";
import base from "../../../vite.config.ts";
import { acceptanceWatchIgnored } from "./acceptance-watch-ignores.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");

// 主检出会积累 target/ 与 .tower/ 产物目录(可达百万级文件):vite dev 默认 watch 整棵
// root、dep 扫描默认按 **/*.html 收集入口,会把产物全部 walk 一遍,heap 涨过 2GB 后 OOM,
// 首个页面加载被拖到 ~29s,30s 的 boot 等待因此踩线超时。这里把 dev server 的视线收回
// 唯一 HTML 入口,并用锚定到 root 的规则跳过产物目录(见 acceptance-watch-ignores.mjs)。
export default mergeConfig(
  base,
  defineConfig({
    optimizeDeps: { entries: ["index.html"] },
    server: { watch: { ignored: acceptanceWatchIgnored(root) } },
  }),
);
