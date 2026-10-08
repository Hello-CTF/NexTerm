import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { acceptanceWatchIgnored } from "./src/test/lib/acceptance-watch-ignores.mjs";

const root = path.dirname(fileURLToPath(import.meta.url));

const config = defineConfig({
  plugins: [react(), tailwindcss()],
  clearScreen: false,
  server: {
    port: 1420,
    strictPort: true,
    // 根目录会积累 .tower/target 等产物目录(主检出可达百万级文件): vite dev 默认
    // watch 整棵 root, 会把产物全部 walk 一遍, heap 冲过 2GB 后 OOM (M238)。忽略
    // 规则锚定 root 本身, 与验收 config 共用 acceptance-watch-ignores.mjs 这一套。
    watch: { ignored: acceptanceWatchIgnored(root) },
  },
  // dep 扫描默认按 **/*.html 收集入口, 同样会走进产物目录; 钉到唯一 HTML 入口。
  // optimizeDeps 与 server 只作用于 dev, production build 输出不受影响。
  optimizeDeps: { entries: ["index.html"] },
  envPrefix: ["VITE_"],
  build: {
    target: "chrome105",
    sourcemap: false,
    chunkSizeWarningLimit: 2000,
  },
  test: {
    environment: "node",
    include: ["src/test/**/*.test.ts", "src/test/**/*.test.tsx"],
    setupFiles: ["src/test/setup.ts"],
    clearMocks: true,
    restoreMocks: true,
    unstubGlobals: true,
  },
});

const include = config.test?.include ?? [];
if (!include.includes("src/test/**/*.test.ts") || !include.includes("src/test/**/*.test.tsx")) {
  throw new Error(
    'test.include 必须同时包含 "src/test/**/*.test.ts" 与 "src/test/**/*.test.tsx"（M62/M63：收窄共享 include 会静默跳过整批 TSX 测试）',
  );
}

export default config;
