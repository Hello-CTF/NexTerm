import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Tauri 前端构建配置：dev 走固定端口，构建产物输出到 dist/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  clearScreen: false,
  server: {
    port: 1420,
    strictPort: true,
    watch: {
      ignored: ["**/src-tauri/**"],
    },
  },
  envPrefix: ["VITE_", "TAURI_ENV_"],
  build: {
    target: "chrome105",
    minify: "esbuild",
    sourcemap: false,
    chunkSizeWarningLimit: 2000,
  },
});
