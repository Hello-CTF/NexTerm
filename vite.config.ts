import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

const config = defineConfig({
  plugins: [react(), tailwindcss()],
  clearScreen: false,
  server: {
    port: 1420,
    strictPort: true,
  },
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
