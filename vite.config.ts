import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

const config = defineConfig({
  plugins: [react(), tailwindcss()],
  clearScreen: false,
  server: {
    port: 1420,
    strictPort: true,
    watch: {
      ignored: ["**/src-tauri/**"],
    },
  },
  envPrefix: ["VITE_"],
  build: {
    target: "chrome105",
    minify: "esbuild",
    sourcemap: false,
    chunkSizeWarningLimit: 2000,
  },
  test: {
    environment: "node",
    include: ["src/test/**/*.test.ts", "src/test/**/*.test.tsx"],
    clearMocks: true,
    restoreMocks: true,
    unstubGlobals: true,
  },
});

// M62 教训：共享 include 曾只收集 .test.ts，M59–M62 的 .test.tsx 被静默跳过；
// 被跳过的 TSX 文件无法自证，只能在配置加载期硬失败。两个字面模式有意与上方
// include 各自书写 —— 守卫必须独立于被检查的值才有意义。
const include = config.test?.include ?? [];
if (!include.includes("src/test/**/*.test.ts") || !include.includes("src/test/**/*.test.tsx")) {
  throw new Error(
    'test.include 必须同时包含 "src/test/**/*.test.ts" 与 "src/test/**/*.test.tsx"（M62/M63：收窄共享 include 会静默跳过整批 TSX 测试）',
  );
}

export default config;
