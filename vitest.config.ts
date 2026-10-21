import { defineConfig, mergeConfig } from "vitest/config";
import baseConfig from "./vite.config.ts";
import { COVERAGE_GATE } from "./src/test/coverage/thresholds.ts";

// Vitest prefers vitest.config.ts over vite.config.ts. Merge the existing
// Vite config so dev server, build and current test behavior stay identical,
// and layer coverage gating on top. Reports go to the gitignored .tmp dir.
// The gate itself (provider, scope, thresholds) lives in
// src/test/coverage/thresholds.ts so the coverage gate test asserts on the
// same object this config ships.
export default mergeConfig(
  baseConfig,
  defineConfig({
    test: {
      coverage: {
        ...COVERAGE_GATE,
        reporter: ["text", "json-summary"],
        reportsDirectory: ".tmp/coverage",
      },
    },
  }),
);
