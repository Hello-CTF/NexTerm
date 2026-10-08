import { defineConfig, mergeConfig } from "vite";
import base from "../../../vite.config.ts";

// 主检出会积累 target/ 与 .tower/ 产物目录(可达百万级文件):vite dev 默认 watch 整棵
// root、dep 扫描默认按 **/*.html 收集入口,会把产物全部 walk 一遍,heap 涨过 2GB 后 OOM,
// 首个页面加载被拖到 ~29s,30s 的 boot 等待因此踩线超时。这里把 dev server 的视线收回
// 唯一 HTML 入口,并跳过与源码无关的产物目录。
export default mergeConfig(
  base,
  defineConfig({
    optimizeDeps: { entries: ["index.html"] },
    server: { watch: { ignored: ["**/.tower/**", "**/target/**", "**/target-alt/**", "**/.tmp/**"] } },
  }),
);
