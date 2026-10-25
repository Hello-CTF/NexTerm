import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { acceptanceWatchIgnored } from "./scripts/lib/vite-watch-ignores.mjs";

const root = path.dirname(fileURLToPath(import.meta.url));

const config = defineConfig({
  plugins: [react(), tailwindcss()],
  clearScreen: false,
  server: {
    port: 1420,
    strictPort: true,
    // The root accumulates artifact dirs like .tower/target (up to millions of
    // files in the main checkout): vite dev watches the whole root by default
    // and would walk all of them, blowing past a 2GB heap into OOM (M238).
    // The ignore rules anchor on the root itself, reusing the shared helper in
    // scripts/lib/vite-watch-ignores.mjs.
    watch: { ignored: acceptanceWatchIgnored(root) },
  },
  // Dep scanning collects entries via **/*.html by default and would also walk
  // into artifact dirs; pin it to the single HTML entry.
  // optimizeDeps and server only affect dev; the production build output is
  // unaffected.
  optimizeDeps: { entries: ["index.html"] },
  envPrefix: ["VITE_"],
  build: {
    target: "chrome105",
    sourcemap: false,
    // Default limit: the entry and every lazy chunk stay under it, so size regressions show up in the build log.
    chunkSizeWarningLimit: 500,
    rolldownOptions: {
      output: {
        // Vendor groups keep their content hash across app-only releases. Language packs
        // (@codemirror/legacy-modes) are deliberately left out so each loads on demand.
        codeSplitting: {
          groups: [
            { name: "vendor-react", test: /node_modules[\\/](react|react-dom|scheduler)[\\/]/, priority: 30 },
            { name: "vendor-xterm", test: /node_modules[\\/]@xterm[\\/]xterm[\\/]/, priority: 20 },
            {
              name: "vendor-codemirror",
              test: /node_modules[\\/](@codemirror[\\/](?!legacy-modes)[^\\/]+|@lezer[\\/][^\\/]+|style-mod|w3c-keyname|crelt)[\\/]/,
              priority: 10,
            },
          ],
        },
      },
    },
  },
  test: {
    maxWorkers: 2,
    environment: "node",
    include: ["src/test/**/*.test.ts", "src/test/**/*.test.tsx"],
    setupFiles: ["src/test/setup.ts"],
    clearMocks: true,
    restoreMocks: true,
    unstubGlobals: true,
  },
});

export default config;
