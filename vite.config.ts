import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// M62 教训：共享 include 曾只收集 .test.ts，M59–M62 的 .test.tsx 被静默跳过。
// 下面的守卫在配置加载时验证 include 仍覆盖两种扩展名（含 features/ 子目录）——
// 一旦被收窄，`pnpm test` / `vite build` 会直接报配置错误，而不是悄悄少跑一批测试。
// src/test/tsxDiscovery.test.tsx 从文本侧钉住同一个契约。
function expandBraces(pattern: string): string[] {
  const match = /\{([^{}]*)\}/.exec(pattern);
  if (!match) return [pattern];
  return match[1]
    .split(",")
    .flatMap((alternative) => expandBraces(pattern.replace(match[0], alternative)));
}

function globToRegExp(glob: string): RegExp {
  const source = glob
    .replace(/[.+^$()|[\]\\]/g, "\\$&")
    .replace(/\*\*\//g, "::globstar-slash::")
    .replace(/\*\*/g, "::globstar::")
    .replace(/\*/g, "[^/]*")
    .replace(/\?/g, "[^/]")
    .replace(/::globstar-slash::/g, "(?:.*/)?")
    .replace(/::globstar::/g, ".*");
  return new RegExp(`^${source}$`);
}

function includeMatches(include: string[], probe: string): boolean {
  return include.some((pattern) =>
    expandBraces(pattern).some((expanded) => globToRegExp(expanded).test(probe)),
  );
}

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

for (const probe of [
  "src/test/__probe__.test.ts",
  "src/test/__probe__.test.tsx",
  "src/test/features/__probe__.test.ts",
  "src/test/features/__probe__.test.tsx",
]) {
  if (!includeMatches(config.test?.include ?? [], probe)) {
    throw new Error(
      `test.include 不再覆盖 ${probe} —— 收窄共享 include 会静默跳过整批测试（M62/M63），请恢复 .test.ts 与 .test.tsx 的双覆盖。`,
    );
  }
}

export default config;
